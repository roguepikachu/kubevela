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
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	webhookCertWaitTimeout  = 60 * time.Second
	webhookCertPollInterval = 1 * time.Second
)

// waitForWebhookCert blocks until tls.crt and tls.key both exist in certDir,
// polling every pollInterval. controller-runtime's webhook server errors
// immediately if these files are missing when the manager starts, and pod
// start order relative to the cert-manager-issued Secret volume landing on
// disk isn't guaranteed, so the manager must not start serving until the
// cert is actually there.
func waitForWebhookCert(certDir string, timeout, pollInterval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if certFilesExist(certDir) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for webhook serving certificate in %s", timeout, certDir)
		}
		time.Sleep(pollInterval)
	}
}

func certFilesExist(certDir string) bool {
	for _, name := range []string{"tls.crt", "tls.key"} {
		if _, err := os.Stat(filepath.Join(certDir, name)); err != nil {
			return false
		}
	}
	return true
}
