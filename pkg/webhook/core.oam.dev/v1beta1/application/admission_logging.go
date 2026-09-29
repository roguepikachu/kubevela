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

package application

import (
	"context"
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/pkg/logging"
)

// withAdmissionLogging runs handle with the request's identity on the context
// and logs one line with the outcome.
func withAdmissionLogging(ctx context.Context, webhook string, req admission.Request, handle func(context.Context) admission.Response) admission.Response {
	start := time.Now()
	ctx = logging.WithFields(ctx,
		"webhook", webhook,
		"admission_uid", string(req.UID),
		"operation", string(req.Operation),
		"app", req.Name,
		"namespace", req.Namespace,
		"user", req.UserInfo.Username,
	)
	ctx = logging.WithLevelFromAnnotations(ctx, requestAnnotations(req))
	log := logging.L(ctx)
	log.Debug("admission request received")

	resp := handle(ctx)

	fields := []any{"allowed", resp.Allowed, "duration_ms", time.Since(start).Milliseconds()}
	if len(resp.Patches) > 0 {
		fields = append(fields, "patches", len(resp.Patches))
	}
	if !resp.Allowed && resp.Result != nil {
		fields = append(fields, "code", resp.Result.Code, "reason", resp.Result.Message)
	}
	log.Info("admission request handled", fields...)
	return resp
}

// requestAnnotations reads only the metadata of the incoming object, so the
// log level override applies before the full decode.
func requestAnnotations(req admission.Request) map[string]string {
	var obj struct {
		Metadata metav1.ObjectMeta `json:"metadata"`
	}
	if err := json.Unmarshal(req.Object.Raw, &obj); err != nil {
		return nil
	}
	return obj.Metadata.Annotations
}
