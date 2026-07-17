/*
Copyright 2026 The KubeVela Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func writeCertFiles(t *testing.T, dir string) {
	t.Helper()
	require := func(err error) {
		if err != nil {
			t.Fatalf("failed to write cert fixture: %v", err)
		}
	}
	require(os.WriteFile(filepath.Join(dir, "tls.crt"), []byte("cert"), 0o600))
	require(os.WriteFile(filepath.Join(dir, "tls.key"), []byte("key"), 0o600))
}

func TestWaitForWebhookCert_AlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	writeCertFiles(t, dir)

	err := waitForWebhookCert(dir, 2*time.Second, 10*time.Millisecond)
	assert.NoError(t, err)
}

func TestWaitForWebhookCert_TimesOut(t *testing.T) {
	dir := t.TempDir()

	err := waitForWebhookCert(dir, 50*time.Millisecond, 10*time.Millisecond)
	assert.Error(t, err)
}

func TestWaitForWebhookCert_AppearsLater(t *testing.T) {
	dir := t.TempDir()

	go func() {
		time.Sleep(30 * time.Millisecond)
		writeCertFiles(t, dir)
	}()

	err := waitForWebhookCert(dir, 2*time.Second, 10*time.Millisecond)
	assert.NoError(t, err)
}
