// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package sourceauth

import "os"

// NewFromEnvironment reads only operator-controlled configuration. There is no
// browser-supplied callback URL, default destination, or fallback credential.
func NewFromEnvironment() (*Client, error) {
	return NewClient(ClientConfig{
		URL:    os.Getenv("ASTARA_SOURCE_AUTH_URL"),
		Secret: os.Getenv("ASTARA_SOURCE_AUTH_SECRET"),
	})
}
