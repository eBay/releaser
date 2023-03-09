/*
Copyright The Kubernetes Authors.

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

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	"context"

	v1alpha2 "github.com/ebay/releaser/pkg/apis/fleet/v1alpha2"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
)

// FakeReleaseEvents implements ReleaseEventInterface
type FakeReleaseEvents struct {
	Fake *FakeFleetV1alpha2
	ns   string
}

var releaseeventsResource = schema.GroupVersionResource{Group: "fleet.crd.tess.io", Version: "v1alpha2", Resource: "releaseevents"}

var releaseeventsKind = schema.GroupVersionKind{Group: "fleet.crd.tess.io", Version: "v1alpha2", Kind: "ReleaseEvent"}

// Get takes name of the releaseEvent, and returns the corresponding releaseEvent object, and an error if there is any.
func (c *FakeReleaseEvents) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1alpha2.ReleaseEvent, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewGetAction(releaseeventsResource, c.ns, name), &v1alpha2.ReleaseEvent{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha2.ReleaseEvent), err
}

// List takes label and field selectors, and returns the list of ReleaseEvents that match those selectors.
func (c *FakeReleaseEvents) List(ctx context.Context, opts v1.ListOptions) (result *v1alpha2.ReleaseEventList, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewListAction(releaseeventsResource, releaseeventsKind, c.ns, opts), &v1alpha2.ReleaseEventList{})

	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1alpha2.ReleaseEventList{ListMeta: obj.(*v1alpha2.ReleaseEventList).ListMeta}
	for _, item := range obj.(*v1alpha2.ReleaseEventList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested releaseEvents.
func (c *FakeReleaseEvents) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewWatchAction(releaseeventsResource, c.ns, opts))

}

// Create takes the representation of a releaseEvent and creates it.  Returns the server's representation of the releaseEvent, and an error, if there is any.
func (c *FakeReleaseEvents) Create(ctx context.Context, releaseEvent *v1alpha2.ReleaseEvent, opts v1.CreateOptions) (result *v1alpha2.ReleaseEvent, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewCreateAction(releaseeventsResource, c.ns, releaseEvent), &v1alpha2.ReleaseEvent{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha2.ReleaseEvent), err
}

// Update takes the representation of a releaseEvent and updates it. Returns the server's representation of the releaseEvent, and an error, if there is any.
func (c *FakeReleaseEvents) Update(ctx context.Context, releaseEvent *v1alpha2.ReleaseEvent, opts v1.UpdateOptions) (result *v1alpha2.ReleaseEvent, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewUpdateAction(releaseeventsResource, c.ns, releaseEvent), &v1alpha2.ReleaseEvent{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha2.ReleaseEvent), err
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *FakeReleaseEvents) UpdateStatus(ctx context.Context, releaseEvent *v1alpha2.ReleaseEvent, opts v1.UpdateOptions) (*v1alpha2.ReleaseEvent, error) {
	obj, err := c.Fake.
		Invokes(testing.NewUpdateSubresourceAction(releaseeventsResource, "status", c.ns, releaseEvent), &v1alpha2.ReleaseEvent{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha2.ReleaseEvent), err
}

// Delete takes name of the releaseEvent and deletes it. Returns an error if one occurs.
func (c *FakeReleaseEvents) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteActionWithOptions(releaseeventsResource, c.ns, name, opts), &v1alpha2.ReleaseEvent{})

	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeReleaseEvents) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	action := testing.NewDeleteCollectionAction(releaseeventsResource, c.ns, listOpts)

	_, err := c.Fake.Invokes(action, &v1alpha2.ReleaseEventList{})
	return err
}

// Patch applies the patch and returns the patched releaseEvent.
func (c *FakeReleaseEvents) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha2.ReleaseEvent, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceAction(releaseeventsResource, c.ns, name, pt, data, subresources...), &v1alpha2.ReleaseEvent{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha2.ReleaseEvent), err
}