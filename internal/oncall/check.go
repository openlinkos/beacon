package oncall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/version"
)

// DrillCheck names the credentials and the enrolled machine for a first drill.
type DrillCheck struct {
	URL         string
	Owner       string
	Agent       string
	Machine     string
	Reporter    string
	AgentToken  string
	ReportToken string
}

type drillTokenView struct {
	Name         string   `json:"name"`
	User         string   `json:"user"`
	Agent        string   `json:"agent"`
	Credential   string   `json:"credential"`
	Subject      string   `json:"subject"`
	Scopes       []string `json:"scopes"`
	MachineScope string   `json:"machine_scope"`
	Machines     []string `json:"machines"`
}

// CheckDrill checks the current credentials and SSH enrolment using the centre's read endpoints.
func CheckDrill(ctx context.Context, check DrillCheck) error {
	base, err := reportBase(check.URL)
	if err != nil {
		return err
	}
	if !validReportToken(check.AgentToken) || !validReportToken(check.ReportToken) {
		return errors.New("each token file must contain one bearer token")
	}
	if check.AgentToken == check.ReportToken {
		return errors.New("the agent token and the report token must be separate")
	}
	if check.Owner == "" || check.Agent == "" || check.Machine == "" || check.Reporter == "" {
		return errors.New("the owner, agent, machine and reporter must be named")
	}

	client := &http.Client{
		Timeout: 4 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	var agent, reporter drillTokenView
	if err := readCentre(ctx, client, base, "token", check.AgentToken, &agent); err != nil {
		return fmt.Errorf("agent token: %w", err)
	}
	if agent.Credential != "token" || agent.Agent != check.Agent || agent.User != check.Owner {
		return fmt.Errorf("the agent token must belong to %q / %q", check.Owner, check.Agent)
	}
	if agent.MachineScope != "listed" || !slices.Equal(agent.Machines, []string{check.Machine}) {
		return fmt.Errorf("the agent token must be limited to %q for this drill", check.Machine)
	}
	for _, scope := range []string{"status:read", "alerts:write", "machines:ssh"} {
		if !slices.Contains(agent.Scopes, scope) {
			return fmt.Errorf("the agent token lacks %s", scope)
		}
	}
	if err := readCentre(ctx, client, base, "token", check.ReportToken, &reporter); err != nil {
		return fmt.Errorf("report token: %w", err)
	}
	if reporter.Credential != "token" || reporter.Name != check.Reporter ||
		reporter.Agent != "" || reporter.User != "" || reporter.Subject != "" {
		return fmt.Errorf("the report token must be the standalone token named %q", check.Reporter)
	}
	if !slices.Equal(reporter.Scopes, []string{"alerts:report"}) {
		return errors.New("the report token must have only the alerts:report scope")
	}

	var channel struct {
		Enabled bool  `json:"enabled"`
		Paused  *bool `json:"paused"`
	}
	if err := readCentre(ctx, client, base, "ssh", check.AgentToken, &channel); err != nil {
		return err
	}
	if !channel.Enabled || channel.Paused == nil || *channel.Paused {
		return errors.New("the centre's SSH channel must be enabled and open")
	}
	var list struct {
		Machines []struct {
			Name      string `json:"name"`
			Reachable bool   `json:"reachable"`
			Connector bool   `json:"connector"`
			Connected bool   `json:"connected"`
		} `json:"machines"`
	}
	if err := readCentre(ctx, client, base, "machines", check.AgentToken, &list); err != nil {
		return err
	}
	for _, machine := range list.Machines {
		if machine.Name != check.Machine {
			continue
		}
		if !machine.Reachable {
			return fmt.Errorf("the agent cannot use the enrolled machine %q", check.Machine)
		}
		if machine.Connector && !machine.Connected {
			return fmt.Errorf("the connector for %q is not connected", check.Machine)
		}
		return nil
	}
	return fmt.Errorf("the machine %q is not enrolled", check.Machine)
}

func readCentre(ctx context.Context, client *http.Client, base *url.URL, endpoint, token string, out any) error {
	path := base.JoinPath("api", "v1", endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path.String(), nil)
	if err != nil {
		return errors.New("could not make the preflight request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("the request for %s failed. Check the centre URL and TLS trust", endpoint)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusUnauthorized {
		return errors.New("the centre refused the credential (HTTP 401). Check the token file and expiry")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("the centre returned HTTP %d for %s", response.StatusCode, endpoint)
	}
	const maxResponse = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse || json.Unmarshal(body, out) != nil {
		return fmt.Errorf("the centre returned an unreadable %s response", endpoint)
	}
	return nil
}
