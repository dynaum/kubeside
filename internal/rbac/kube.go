package rbac

import (
	"context"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// KubeReviewer asks the apiserver, which is the only authority on what this
// reader may do.
type KubeReviewer struct {
	// ClientFor returns the client for one context. A context with no client is
	// one kubeside has not connected, and an unconnected cluster cannot grant
	// anything.
	ClientFor func(contextName string) (kubernetes.Interface, bool)
}

// Review runs a SelfSubjectAccessReview.
func (k KubeReviewer) Review(ctx context.Context, contextName string, a Action) (bool, error) {
	client, ok := k.ClientFor(contextName)
	if !ok {
		return false, errNotConnected(contextName)
	}

	review := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace:   a.Namespace,
				Verb:        a.Verb,
				Group:       a.Group,
				Resource:    a.Resource,
				Subresource: a.Subresource,
				Name:        a.Name,
			},
		},
	}
	got, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return got.Status.Allowed, nil
}

type notConnectedError string

func (e notConnectedError) Error() string { return "context " + string(e) + " is not connected" }

func errNotConnected(name string) error { return notConnectedError(name) }
