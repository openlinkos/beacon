package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/openlinkos/beacon/internal/exercise"
)

func (a *Agent) reportExercise(ctx context.Context, receipt string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"receipt": receipt})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.Gateway+exercise.ObservationPath, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.cfg.HTTP.Do(req)
	if err != nil {
		a.cfg.Log.Warn("could not report a drill observation")
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		a.cfg.Log.Warn("drill observation was declined", "status", resp.StatusCode)
	}
}
