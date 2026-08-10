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

package spokecluster

import (
	"context"
	"time"

	pkgcache "github.com/kubevela/pkg/cache"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

// DefaultCredentialCacheTTL caps how long any entry may be served, whatever refresh
// deadline a provider reports. NextRefresh already bounds every entry, so this only
// guards against a provider returning an implausibly distant deadline and pinning a
// credential in memory. It is the default for --credential-cache-ttl.
//
// 15 minutes matches the EKS presign window, the shortest credential lifetime any
// provider produces today, so the clamp never shortens a real deadline in practice.
const DefaultCredentialCacheTTL = 15 * time.Minute

// credentialCacheEntry is one spoke's last materialized credential, plus everything
// needed to decide whether it still describes that spoke.
type credentialCacheEntry struct {
	m *credential.Materialized
	// generation and uid together answer "same object, unchanged". Generation alone is
	// not enough: deleting and recreating under one name resets it to 1, which is what a
	// first-generation entry already holds.
	generation int64
	uid        apitypes.UID
	// deadline is the earlier of the credential's own NextRefresh and the TTL clamp.
	deadline time.Time
}

// credentialCache holds materialized credentials between reconciles so a pass whose
// credential is still well short of its refresh deadline can skip Materialize entirely.
// For the aws arm that call is an sts:AssumeRole plus an eks:DescribeCluster every pass.
//
// Storage is delegated to the shared in-memory store, which also sweeps expired entries.
// The freshness decision is not delegated: the store evaluates expiry against its own
// time.Now(), which no test can control, and it treats a non-positive duration as "never
// expires", the inverse of the rule that a zero NextRefresh must never be cached. Owning
// the comparison here keeps both properties testable and explicit.
//
// A nil *credentialCache is a working cache with caching switched off: every method is
// nil-safe and Get always misses. That is what lets a Reconciler built directly, as the
// unit tests do, behave exactly as it did before this type existed.
type credentialCache struct {
	store pkgcache.Cache[apitypes.NamespacedName]
	// ttl caps how long an entry may be served regardless of the deadline its provider
	// reported. Operator-tunable through --credential-cache-ttl.
	ttl time.Duration
	// now is the clock seam. Refresh math is the whole point of this type, so it has to
	// be assertable without sleeping.
	now func() time.Time
}

// newCredentialCache builds a cache whose sweeper goroutine lives as long as ctx, and
// whose entries are never served for longer than ttl.
//
// A ttl of zero or less returns nil, which is a working cache with caching switched off.
// That is the operator escape hatch behind --credential-cache-ttl=0: it restores the
// pre-cache behaviour of re-deriving a credential on every pass, without a rebuild.
func newCredentialCache(ctx context.Context, ttl time.Duration) *credentialCache {
	if ttl <= 0 {
		return nil
	}
	return &credentialCache{
		store: pkgcache.NewMemoryCacheStore[apitypes.NamespacedName](ctx),
		ttl:   ttl,
		now:   time.Now,
	}
}

// Get returns the cached credential for sc when it is still safe to reuse.
//
// margin is how much remaining validity the caller needs: an entry is treated as stale
// that far ahead of its deadline, so whatever comes back outlives the next scheduled
// reconcile rather than merely outliving this instant. See the call site in
// reconcileConnect, which passes one probe interval.
func (c *credentialCache) Get(sc *v1beta1.SpokeCluster, margin time.Duration) (*credential.Materialized, bool) {
	if c == nil {
		return nil, false
	}
	value, found := c.store.Get(client.ObjectKeyFromObject(sc))
	if !found {
		return nil, false
	}
	entry, ok := value.(credentialCacheEntry)
	switch {
	case !ok:
		// Nothing else writes this store, so a foreign type means a bug rather than a
		// condition to recover from. Treat it as a miss: re-materializing is always safe.
		return nil, false
	case entry.uid != sc.UID:
		// Same name, different object: deleted and recreated.
		return nil, false
	case entry.generation != sc.Generation:
		// The spec moved, so this credential is not the one now declared.
		return nil, false
	case !c.now().Add(margin).Before(entry.deadline):
		return nil, false
	}
	return entry.m.DeepCopy(), true
}

// Put stores a credential, or declines to.
//
// A zero NextRefresh means "do not cache", never "cache forever". It marks a credential
// whose source can change underneath the controller with nothing to signal it, which is
// every kubeconfig spoke. Those re-read a hub Secret through the cached client and parse
// it, so there is nothing to save by caching and a rotated Secret would stop being picked
// up if it were cached.
func (c *credentialCache) Put(sc *v1beta1.SpokeCluster, m *credential.Materialized) {
	if c == nil || m == nil || m.NextRefresh.IsZero() {
		return
	}
	now := c.now()
	deadline := m.NextRefresh
	if clamp := now.Add(c.ttl); deadline.After(clamp) {
		deadline = clamp
	}
	if !deadline.After(now) {
		// Already past due. Storing it would only guarantee a miss.
		return
	}
	c.store.Put(client.ObjectKeyFromObject(sc), credentialCacheEntry{
		m:          m.DeepCopy(),
		generation: sc.Generation,
		uid:        sc.UID,
		deadline:   deadline,
	}, deadline.Sub(now))
}

// Invalidate drops a spoke's entry. It is idempotent, so any path may call it
// unconditionally.
func (c *credentialCache) Invalidate(key apitypes.NamespacedName) {
	if c == nil {
		return
	}
	c.store.Delete(key)
}
