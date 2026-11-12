package service

import "testing"

func TestRequireAccountDispatchWritesEnabled(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		t.Setenv(accountDispatchWritesEnv, "")
		if err := requireAccountDispatchWritesEnabled(); err == nil {
			t.Fatal("expected writes to be disabled")
		}
	})

	for _, value := range []string{"1", "true", "TRUE"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(accountDispatchWritesEnv, value)
			if err := requireAccountDispatchWritesEnabled(); err != nil {
				t.Fatalf("expected writes to be enabled: %v", err)
			}
		})
	}
}
