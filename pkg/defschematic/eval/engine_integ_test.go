package eval_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	velaprocess "github.com/oam-dev/kubevela/pkg/cue/process"
	"github.com/oam-dev/kubevela/pkg/defschematic/eval"
	"github.com/oam-dev/kubevela/pkg/defschematic/pocdefs"
)

func TestEngineComponentThenTrait(t *testing.T) {
	pCtx := velaprocess.NewContext(velaprocess.ContextData{
		CompName:  "frontend",
		Namespace: "default",
		AppName:   "dir-poc-app",
	})
	comp := pocdefs.MustDefkitIR(pocdefs.DefkitWebservice())
	b, err := comp.ToJSON()
	require.NoError(t, err)
	require.NoError(t, eval.NewWorkloadEngine("dir-webservice").Complete(pCtx, string(b), map[string]interface{}{
		"image": "nginx:1.27-alpine", "port": int64(80), "replicas": int64(1),
	}))
	base, aux := pCtx.Output()
	require.NotNil(t, base)
	u, err := base.Unstructured()
	require.NoError(t, err)
	require.Equal(t, "Deployment", u.GetKind())
	require.NotEmpty(t, aux)

	tr := pocdefs.MustDefkitIR(pocdefs.DefkitScaler())
	tb, err := tr.ToJSON()
	require.NoError(t, err)
	require.NoError(t, eval.NewTraitEngine("dir-scaler").Complete(pCtx, string(tb), map[string]interface{}{
		"replicas": int64(2),
	}))
	base2, _ := pCtx.Output()
	u2, err := base2.Unstructured()
	require.NoError(t, err)
	replicas, found, err := unstructuredNestedInt(u2.Object, "spec", "replicas")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(2), replicas)
}
