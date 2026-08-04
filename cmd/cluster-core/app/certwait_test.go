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
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCertFiles(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), []byte("cert"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "tls.key"), []byte("key"), 0o600)
}

var _ = It("WaitForWebhookCert AlreadyPresent", func() {
	t := GinkgoT()
	dir := t.TempDir()
	require.NoError(t, writeCertFiles(dir))

	err := waitForWebhookCert(dir, 2*time.Second, 10*time.Millisecond)
	assert.NoError(t, err)
})

var _ = It("WaitForWebhookCert TimesOut", func() {
	t := GinkgoT()
	dir := t.TempDir()

	err := waitForWebhookCert(dir, 50*time.Millisecond, 10*time.Millisecond)
	assert.Error(t, err)
})

var _ = It("WaitForWebhookCert AppearsLater", func() {
	t := GinkgoT()
	dir := t.TempDir()
	writeErr := make(chan error, 1)

	go func() {
		time.Sleep(30 * time.Millisecond)
		writeErr <- writeCertFiles(dir)
	}()

	err := waitForWebhookCert(dir, 2*time.Second, 10*time.Millisecond)
	assert.NoError(t, err)
	require.NoError(t, <-writeErr)
})
