//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type failureBillingSettingRepo struct {
	value    string
	getCalls int
	updates  map[string]string
	setError error
}

func (r *failureBillingSettingRepo) Get(context.Context, string) (*Setting, error) {
	panic("unexpected Get call")
}

func (r *failureBillingSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.getCalls++
	if key != SettingKeyFailureBillingUpstreamUsageOnly {
		panic("unexpected setting key: " + key)
	}
	if r.value == "" {
		return "", ErrSettingNotFound
	}
	return r.value, nil
}

func (r *failureBillingSettingRepo) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (r *failureBillingSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

func (r *failureBillingSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	r.updates = make(map[string]string, len(values))
	for key, value := range values {
		r.updates[key] = value
	}
	return r.setError
}

func (r *failureBillingSettingRepo) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (r *failureBillingSettingRepo) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func TestFailureBillingSettingParsesFalseByDefaultAndTrueExplicitly(t *testing.T) {
	svc := &SettingService{cfg: &config.Config{}}
	if got := svc.parseSettings(map[string]string{}).FailureBillingUpstreamUsageOnly; got {
		t.Fatal("missing failure billing policy must default to false")
	}
	if got := svc.parseSettings(map[string]string{
		SettingKeyFailureBillingUpstreamUsageOnly: "true",
	}).FailureBillingUpstreamUsageOnly; !got {
		t.Fatal("explicit true failure billing policy must be preserved")
	}
}

func TestFailureBillingSettingPersistsAndRefreshesCache(t *testing.T) {
	repo := &failureBillingSettingRepo{}
	svc := &SettingService{settingRepo: repo, cfg: &config.Config{}}
	if err := svc.UpdateSettings(context.Background(), &SystemSettings{
		FailureBillingUpstreamUsageOnly: true,
	}); err != nil {
		t.Fatalf("UpdateSettings failed: %v", err)
	}
	if got := repo.updates[SettingKeyFailureBillingUpstreamUsageOnly]; got != "true" {
		t.Fatalf("persisted value = %q, want true", got)
	}
	if got := svc.GetFailureBillingUpstreamUsageOnlyCached(context.Background()); !got {
		t.Fatal("updated cache must report true")
	}
}

func TestFailureBillingSettingCacheReadsRepositoryOnceUntilExpiry(t *testing.T) {
	repo := &failureBillingSettingRepo{value: "true"}
	svc := &SettingService{settingRepo: repo, cfg: &config.Config{}}
	if !svc.GetFailureBillingUpstreamUsageOnlyCached(context.Background()) {
		t.Fatal("initial repository value must be returned")
	}
	repo.value = "false"
	if !svc.GetFailureBillingUpstreamUsageOnlyCached(context.Background()) {
		t.Fatal("cached value must remain stable during its TTL")
	}
	if repo.getCalls != 1 {
		t.Fatalf("repository reads = %d, want 1", repo.getCalls)
	}
}
