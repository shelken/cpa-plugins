package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestReloginPreservesNewCredentials(t *testing.T) {
	c, err := parseCredential([]byte(`{"userId":"same-user","machineId":"new-machine","credentials":{"access":"new-access","refresh":"new-refresh","expires":2000000000000}}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.ToAuthData("same-user.json")
	if err != nil {
		t.Fatal(err)
	}
	record := &coreauth.Auth{Metadata: data.Metadata}
	coreauth.MergeExistingAuthMetadata(record, map[string]any{
		"credentials": map[string]any{"access": "old-access", "refresh": "old-refresh", "expires": 1000000000000},
		"machineId":   "old-machine", "sessionState": "old-session", "scope": "old-scope", "domain": "old-domain", "weight": float64(7),
	})
	var persisted map[string]any
	if err := json.Unmarshal(data.StorageJSON, &persisted); err != nil {
		t.Fatal(err)
	}
	for k, v := range record.Metadata {
		persisted[k] = v
	}
	raw, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := parseCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	if restored.AccessToken() != "new-access" || restored.RefreshToken() != "new-refresh" || restored.Expires() != 2000000000000 {
		t.Fatal("old credentials overwrote new login")
	}
	if restored.MachineID != "new-machine" {
		t.Fatal("old device identity overwrote new login")
	}
	if persisted["weight"] != float64(7) {
		t.Fatal("user setting lost")
	}
}

type refreshObserver struct {
	coreauth.ProviderExecutor
	provider  string
	refreshed chan *coreauth.Auth
}

func (e *refreshObserver) Identifier() string { return e.provider }
func (e *refreshObserver) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	select {
	case e.refreshed <- a.Clone():
	default:
	}
	return a.Clone(), nil
}

func TestExpiredCredentialAutomaticallyRefreshes(t *testing.T) {
	c, err := parseCredential([]byte(`{"userId":"expired-user","machineId":"machine","credentials":{"access":"expired-access","refresh":"valid-refresh","expires":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.ToAuthData("expired-user.json")
	if err != nil {
		t.Fatal(err)
	}
	observer := &refreshObserver{provider: data.Provider, refreshed: make(chan *coreauth.Auth, 1)}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(observer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = manager.Register(ctx, &coreauth.Auth{ID: data.ID, Provider: data.Provider, Metadata: data.Metadata, NextRefreshAfter: data.NextRefreshAfter, Status: coreauth.StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	manager.StartAutoRefresh(ctx, 10*time.Millisecond)
	defer manager.StopAutoRefresh()
	select {
	case got := <-observer.refreshed:
		if got.Metadata["credentials"].(map[string]any)["refresh"] != "valid-refresh" {
			t.Fatal("refresh received wrong credential")
		}
	case <-time.After(time.Second):
		t.Fatal("expired credential never reached executor refresh")
	}
}
