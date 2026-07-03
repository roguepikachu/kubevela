/*
 Copyright 2026. The KubeVela Authors.

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
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const (
	// eksTokenPrefix is the scheme EKS expects on the bearer token it validates.
	eksTokenPrefix = "k8s-aws-v1."
	// clusterIDHeader is the header EKS binds the presigned URL to, so a token minted for one
	// cluster cannot be replayed against another.
	clusterIDHeader = "x-k8s-aws-id"
	// presignExpiry is the presigned URL validity window that AWS enforces (15 minutes).
	presignExpiry = 15 * time.Minute
	// tokenRefreshLead is how long before presignExpiry the controller remints the token.
	tokenRefreshLead = 1 * time.Minute
	// stsGetCallerIdentityAction is the action encoded in the presigned request.
	stsGetCallerIdentityAction = "Action=GetCallerIdentity&Version=2011-06-15"
)

// stsPresignAPI is the subset of the STS presign client the token generator needs. It is an
// interface so tests can supply a deterministic presigner without calling AWS.
type stsPresignAPI interface {
	PresignGetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// generateEKSToken mints an EKS bearer token by presigning an STS GetCallerIdentity request bound
// to the cluster via the x-k8s-aws-id header, then base64url-encoding the presigned URL with the
// k8s-aws-v1. prefix. It returns the token and the time at which it should be reminted.
//
// The algorithm matches `aws eks get-token`: EKS validates the token by replaying the presigned
// URL against STS and checking the returned identity and the cluster-id header.
func generateEKSToken(ctx context.Context, presigner stsPresignAPI, clusterName string, now time.Time) (string, time.Time, error) {
	if clusterName == "" {
		return "", time.Time{}, fmt.Errorf("clusterName is required to mint an EKS token")
	}
	presigned, err := presigner.PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}, func(o *sts.PresignOptions) {
		o.ClientOptions = append(o.ClientOptions, func(so *sts.Options) {
			so.APIOptions = append(so.APIOptions, smithyhttp.SetHeaderValue(clusterIDHeader, clusterName))
		})
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to presign STS GetCallerIdentity for cluster %q: %w", clusterName, err)
	}
	token := eksTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(presigned.URL))
	refreshAt := now.Add(presignExpiry - tokenRefreshLead)
	return token, refreshAt, nil
}

// signerFromCredentials builds an STS presign client from static credentials. It is used by the
// AWS provider after assuming the per-cluster role.
func signerFromCredentials(region string, creds aws.CredentialsProvider) stsPresignAPI {
	cfg := aws.Config{Region: region, Credentials: creds}
	return sts.NewPresignClient(sts.NewFromConfig(cfg))
}

// decodeEKSTokenURL is a test helper that recovers the presigned URL from a minted token. It lives
// here (not in _test.go) so both the generator and its tests share one definition of the format.
func decodeEKSTokenURL(token string) (string, error) {
	if !strings.HasPrefix(token, eksTokenPrefix) {
		return "", fmt.Errorf("token missing %q prefix", eksTokenPrefix)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, eksTokenPrefix))
	if err != nil {
		return "", fmt.Errorf("token payload is not base64url: %w", err)
	}
	return string(raw), nil
}
