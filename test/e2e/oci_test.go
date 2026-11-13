package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/argoproj/argo-cd/gitops-engine/v3/pkg/health"
	. "github.com/argoproj/argo-cd/gitops-engine/v3/pkg/sync/common"
	imagev1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/test/e2e/fixture"
	. "github.com/argoproj/argo-cd/v3/test/e2e/fixture/app"
	"github.com/argoproj/argo-cd/v3/test/e2e/fixture/repos"
)

func TestOCIImage(t *testing.T) {
	Given(t).
		RepoURLType(fixture.RepoURLTypeOCI).
		PushImageToOCIRegistry("testdata/guestbook", "1.0.0").
		OCIRepoAdded("guestbook", "guestbook").
		Revision("1.0.0").
		OCIRegistry(fixture.OCIHostURL).
		OCIRegistryPath("guestbook").
		Path(".").
		When().
		CreateApp().
		Then().
		Expect(SyncStatusIs(SyncStatusCodeOutOfSync)).
		Expect(Success("")).
		When().
		Sync().
		Then().
		Expect(Success("")).
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(SyncStatusIs(SyncStatusCodeSynced)).
		Expect(HealthIs(health.HealthStatusHealthy))
}

func TestOCIImageLayerSelector(t *testing.T) {
	ctx := Given(t).RepoURLType(fixture.RepoURLTypeOCI).Path("manifests/dev")
	const annotation = "io.argoproj.e2e.layer"
	layers := make([]repos.OCILayer, 0, 2)
	for _, name := range []string{"first", "second"} {
		layerPath := t.TempDir()
		manifestPath := filepath.Join(layerPath, "manifests", "dev")
		require.NoError(t, os.MkdirAll(manifestPath, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(manifestPath, "configmaps.yaml"), []byte(fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: selected-config
data:
  layer: %s
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: %s-only
`, name, name)), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(layerPath, "outside-path.yaml"), []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: outside-path
`), 0o644))
		layers = append(layers, repos.OCILayer{Path: layerPath, Annotations: map[string]string{annotation: name}})
	}
	revision := repos.PushMultiLayerImageToOCIRegistry(t, ctx.AppName(), "1.0.0", layers...)

	checkResources := func(app *Application, selected, unselected string) {
		require.Equal(t, revision, app.Spec.Source.TargetRevision)
		require.Len(t, app.Status.Resources, 2)
		for _, resource := range app.Status.Resources {
			require.Equal(t, "ConfigMap", resource.Kind)
			require.Contains(t, []string{"selected-config", selected + "-only"}, resource.Name)
		}
		configMaps := fixture.KubeClientset.CoreV1().ConfigMaps(ctx.DeploymentNamespace())
		configMap, err := configMaps.Get(t.Context(), "selected-config", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, selected, configMap.Data["layer"])
		_, err = configMaps.Get(t.Context(), selected+"-only", metav1.GetOptions{})
		require.NoError(t, err)
		for _, name := range []string{unselected + "-only", "outside-path"} {
			_, err := configMaps.Get(t.Context(), name, metav1.GetOptions{})
			require.True(t, apierrors.IsNotFound(err), "ConfigMap %s must not exist: %v", name, err)
		}
	}

	ctx.OCIRepoAdded(ctx.AppName(), ctx.AppName()).
		When().
		CreateFromFile(func(app *Application) {
			app.Spec.Source.RepoURL = fixture.OCIHostURL + "/" + ctx.AppName()
			app.Spec.Source.TargetRevision = revision
			app.Spec.Source.OCI = &ApplicationSourceOCI{LayerSelector: &OCILayerSelector{
				MediaType:   imagev1.MediaTypeImageLayerGzip,
				Annotations: map[string]string{annotation: "first"},
			}}
		}).
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(SyncStatusIs(SyncStatusCodeSynced)).
		Expect(SyncRevisionIs(revision)).
		And(func(app *Application) { checkResources(app, "first", "second") }).
		When().
		PatchApp(`[{"op":"replace","path":"/spec/source/oci/layerSelector/annotations/io.argoproj.e2e.layer","value":"second"}]`).
		Then().
		Expect(SyncStatusIs(SyncStatusCodeOutOfSync)).
		When().
		Sync("--prune").
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(SyncStatusIs(SyncStatusCodeSynced)).
		Expect(SyncRevisionIs(revision)).
		And(func(app *Application) { checkResources(app, "second", "first") })
}

func TestOCIWithOCIHelmRegistryDependencies(t *testing.T) {
	Given(t).
		RepoURLType(fixture.RepoURLTypeOCI).
		PushChartToOCIRegistry("testdata/helm-values", "helm-values", "1.0.0").
		PushImageToOCIRegistry("testdata/helm-oci-with-dependencies", "1.0.0").
		OCIRegistry(fixture.OCIHostURL).
		OCIRepoAdded("helm-oci-with-dependencies", "helm-oci-with-dependencies").
		OCIRepoAdded("helm-values", "myrepo").
		OCIRegistryPath("helm-oci-with-dependencies").
		Revision("1.0.0").
		Path(".").
		When().
		CreateApp().
		Then().
		When().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(HealthIs(health.HealthStatusHealthy)).
		Expect(SyncStatusIs(SyncStatusCodeSynced))
}

func TestOCIWithAuthedOCIHelmRegistryDeps(t *testing.T) {
	Given(t).
		RepoURLType(fixture.RepoURLTypeOCI).
		PushChartToAuthenticatedOCIRegistry("testdata/helm-values", "helm-values", "1.0.0").
		PushImageToOCIRegistry("testdata/helm-oci-authed-with-dependencies", "1.0.0").
		OCIRepoAdded("helm-oci-authed-with-dependencies", "helm-oci-authed-with-dependencies").
		AuthenticatedOCIRepoAdded("helm-values", "myrepo/helm-values").
		OCIRegistry(fixture.OCIHostURL).
		OCIRegistryPath("helm-oci-authed-with-dependencies").
		Revision("1.0.0").
		Path(".").
		When().
		CreateApp().
		Then().
		When().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(HealthIs(health.HealthStatusHealthy)).
		Expect(SyncStatusIs(SyncStatusCodeSynced))
}

func TestOCIImageWithOutOfBoundsSymlink(t *testing.T) {
	Given(t).
		RepoURLType(fixture.RepoURLTypeOCI).
		PushImageToOCIRegistry("testdata3/symlink-out-of-bounds", "1.0.0").
		OCIRepoAdded("symlink-out-of-bounds", "symlink-out-of-bounds").
		Revision("1.0.0").
		OCIRegistry(fixture.OCIHostURL).
		OCIRegistryPath("symlink-out-of-bounds").
		Path(".").
		When().
		IgnoreErrors().
		CreateApp().
		Then().
		Expect(ErrorRegex("", "could not decompress layer: error checking symlink .* target: .* path escapes from parent"))
}
