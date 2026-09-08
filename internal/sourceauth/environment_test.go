// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package sourceauth

import (
	"strings"
	"testing"
)

func TestConfigRejectsEmptyHostnameAndQueryDelimiter(t *testing.T) {
	for _, endpoint := range []string{"http://:8080/authorize", "https://plane/authorize?"} {
		if client, err := NewClient(ClientConfig{URL: endpoint, Secret: strings.Repeat("s", 32)}); err == nil || client != nil {
			t.Fatalf("invalid endpoint accepted: %s", endpoint)
		}
	}
}

func TestEnvironmentNeverFallsBackToAnotherCredential(t *testing.T) {
	t.Setenv("ASTARA_SOURCE_AUTH_URL", "http://plane-internal/api/internal/knowledge/v1/authorize/")
	t.Setenv("ASTARA_SOURCE_AUTH_SECRET", "")
	t.Setenv("ASTARA_SERVICE_AUTH_SECRET", strings.Repeat("s", 32))
	t.Setenv("ASTARA_IDENTITY_EXCHANGE_SECRET", strings.Repeat("i", 32))
	if client, err := NewFromEnvironment(); err == nil || client != nil {
		t.Fatal("missing dedicated key accepted")
	}
	t.Setenv("ASTARA_SOURCE_AUTH_SECRET", strings.Repeat("a", 32))
	if client, err := NewFromEnvironment(); err != nil || client == nil {
		t.Fatal("explicit valid configuration rejected")
	}
	t.Setenv("ASTARA_SOURCE_AUTH_URL", "")
	if client, err := NewFromEnvironment(); err == nil || client != nil {
		t.Fatal("missing endpoint accepted")
	}
}
