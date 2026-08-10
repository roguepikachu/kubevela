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

package credential

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	. "github.com/onsi/ginkgo/v2"
)

// fakePresigner is a deterministic stand-in for the STS presign client.
type fakePresigner struct {
	url string
	err error
}

func (f *fakePresigner) PresignGetCallerIdentity(_ context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &v4.PresignedHTTPRequest{URL: f.url, Method: "GET"}, nil
}

var _ = It("GenerateEKSToken", func() {
	t := GinkgoT()
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	presignedURL := "https://sts.us-east-1.amazonaws.com/?Action=GetCallerIdentity&Version=2011-06-15&X-Amz-Signature=abc"

	token, refreshAt, err := generateEKSToken(context.Background(), &fakePresigner{url: presignedURL}, "prod-us-east-1", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(token, eksTokenPrefix) {
		t.Fatalf("token %q missing prefix %q", token, eksTokenPrefix)
	}
	// The token must contain no base64 padding (RawURLEncoding), since EKS rejects '=' in tokens.
	if strings.Contains(token, "=") {
		t.Fatalf("token %q must not contain base64 padding", token)
	}
	// The decoded payload must be the exact presigned URL.
	decoded, err := decodeEKSTokenURL(token)
	if err != nil {
		t.Fatalf("failed to decode token: %v", err)
	}
	if decoded != presignedURL {
		t.Fatalf("decoded URL = %q, want %q", decoded, presignedURL)
	}
	// Refresh must land tokenRefreshLead before the 15-minute STS window closes.
	wantRefresh := now.Add(13 * time.Minute)
	if !refreshAt.Equal(wantRefresh) {
		t.Fatalf("refreshAt = %v, want %v", refreshAt, wantRefresh)
	}
})

var _ = It("EKSTokenLifetimeAlignedWithRefresh", func() {
	t := GinkgoT()
	// X-Amz-Expires must describe the same 15-minute window NextRefresh is derived from.
	if want := int(presignExpiry / time.Second); presignTokenExpirySeconds != want {
		t.Fatalf("presignTokenExpirySeconds = %d, want %d (presignExpiry)", presignTokenExpirySeconds, want)
	}
	if got := presignExpiry - tokenRefreshLead; got != 13*time.Minute {
		t.Fatalf("presignExpiry-tokenRefreshLead = %v, want 13m", got)
	}
	if presignTokenExpirySeconds > 900 {
		t.Fatalf("X-Amz-Expires %d exceeds aws-iam-authenticator max 900", presignTokenExpirySeconds)
	}
})

var _ = It("SetExpiresMiddlewareInjectsQuery", func() {
	t := GinkgoT()
	raw, err := http.NewRequest(http.MethodGet, "https://sts.amazonaws.com/?Action=GetCallerIdentity", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	sreq := &smithyhttp.Request{Request: raw}
	mw := setExpiresMiddleware{seconds: presignTokenExpirySeconds}
	next := middleware.BuildHandlerFunc(func(ctx context.Context, in middleware.BuildInput) (middleware.BuildOutput, middleware.Metadata, error) {
		got, ok := in.Request.(*smithyhttp.Request)
		if !ok {
			t.Fatal("expected smithyhttp.Request")
		}
		if v := got.URL.Query().Get("X-Amz-Expires"); v != strconv.Itoa(presignTokenExpirySeconds) {
			t.Fatalf("X-Amz-Expires = %q, want %d", v, presignTokenExpirySeconds)
		}
		return middleware.BuildOutput{}, middleware.Metadata{}, nil
	})
	if _, _, err := mw.HandleBuild(context.Background(), middleware.BuildInput{Request: sreq}, next); err != nil {
		t.Fatalf("HandleBuild: %v", err)
	}
})

var _ = It("GenerateEKSTokenErrors", func() {
	t := GinkgoT()
	now := time.Now()
	if _, _, err := generateEKSToken(context.Background(), &fakePresigner{url: "https://x"}, "", now); err == nil {
		t.Fatal("expected error for empty cluster name")
	}
	presignErr := errors.New("sts down")
	if _, _, err := generateEKSToken(context.Background(), &fakePresigner{err: presignErr}, "c", now); err == nil {
		t.Fatal("expected error when presign fails")
	}
})

var _ = It("DecodeEKSTokenURL", func() {
	t := GinkgoT()
	if _, err := decodeEKSTokenURL("not-a-token"); err == nil {
		t.Fatal("expected error for token without prefix")
	}
	if _, err := decodeEKSTokenURL(eksTokenPrefix + "!!not-base64!!"); err == nil {
		t.Fatal("expected error for non-base64url payload")
	}
})
