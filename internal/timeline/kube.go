package timeline

import (
	"context"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata"
)

// KubeReleases lists Helm's release Secrets as metadata.
//
// The metadata client sends Accept: application/json;as=PartialObjectMetadata,
// so the apiserver strips everything but the object's metadata before it
// answers. That is the whole point: this package wants four labels and a
// creation timestamp, and a helm.sh/release.v1 payload is a gzipped release
// whose chart values routinely carry real credentials. Asking for less means
// less crosses the wire, and nothing that matters ever reaches this process.
type KubeReleases struct {
	Client metadata.Interface
}

// ReleasesUnavailable stands in when no metadata reader could be built for a
// context. It fails rather than returning nothing, because "no releases" and "I
// could not look" are different facts and the timeline renders them differently.
type ReleasesUnavailable struct{ Reason string }

// ListReleases always fails, carrying the reason.
func (r ReleasesUnavailable) ListReleases(context.Context, string, string) ([]metav1.PartialObjectMetadata, error) {
	return nil, errors.New(r.Reason)
}

var secretsResource = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// ListReleases returns the metadata of the secrets matching selector.
func (k KubeReleases) ListReleases(ctx context.Context, namespace, selector string) ([]metav1.PartialObjectMetadata, error) {
	list, err := k.Client.Resource(secretsResource).Namespace(namespace).
		List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}
