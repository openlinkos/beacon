package oncall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
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
	Hook        string
	Chat        string
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

var drillHookName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

var errCentreNotFound = errors.New("the centre returned HTTP 404")

// CheckDrill checks credentials, SSH enrolment and optionally the loaded hook and critical chat configuration.
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

	if check.Hook != "" && !drillHookName.MatchString(check.Hook) {
		return errors.New("the hook name uses lower case letters, digits, dots, dashes and underscores")
	}
	if check.Chat != "" {
		if check.Hook == "" {
			return errors.New("-chat requires -hook")
		}
		if !drillChatRoute(check.Chat) {
			return errors.New("-chat must be app, webhook, notification_webhook or slack")
		}
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
		if check.Hook != "" {
			return checkLiveDrillHook(ctx, client, base, check)
		}
		return nil
	}
	return fmt.Errorf("the machine %q is not enrolled", check.Machine)
}

func drillChatRoute(route string) bool {
	switch route {
	case "app", "webhook", "notification_webhook", "slack":
		return true
	default:
		return false
	}
}

func checkLiveDrillHook(ctx context.Context, client *http.Client, base *url.URL, check DrillCheck) error {
	var hook struct {
		SignedWebhook bool   `json:"signed_webhook"`
		MatchesDrill  bool   `json:"matches_drill"`
		DrillOnly     bool   `json:"drill_only"`
		CriticalChat  string `json:"critical_chat"`
	}
	err := readCentre(ctx, client, base, "agent-hooks/"+check.Hook+"/first-drill", check.ReportToken, &hook)
	switch {
	case errors.Is(err, errCentreNotFound):
		return fmt.Errorf("the centre did not confirm hook %q for this reporter. Check the loaded hook name, reporter and release", check.Hook)
	case err != nil:
		return err
	case !hook.SignedWebhook:
		return errors.New("the loaded hook must be a webhook with a signing secret for beacon-oncall")
	case !hook.MatchesDrill:
		return errors.New("the loaded hook does not match a firing critical alert for service drill")
	case !hook.DrillOnly:
		return errors.New("limit the first hook to service drill or ^drill$ before running the drill")
	case hook.CriticalChat == "none":
		return errors.New("there is no configured critical chat route. Apply the test chat configuration first")
	case !drillChatRoute(hook.CriticalChat):
		return errors.New("the centre returned an unsupported critical chat route")
	case check.Chat != "" && hook.CriticalChat != check.Chat:
		return fmt.Errorf("the configured critical chat route is %q; expected %q", hook.CriticalChat, check.Chat)
	}
	return nil
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
	if response.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w for %s", errCentreNotFound, endpoint)
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
