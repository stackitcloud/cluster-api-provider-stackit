/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package shared

import (
	"context"
	"fmt"
	"testing"
)

func TestErrorClassification(t *testing.T) {
	if !IsNotFound(fmt.Errorf("wrapped: %w", ErrNotFound)) {
		t.Fatal("IsNotFound() = false, want true")
	}
	if !IsUnauthorized(fmt.Errorf("wrapped: %w", ErrUnauthorized)) {
		t.Fatal("IsUnauthorized() = false, want true")
	}
	if !IsInvalidInput(fmt.Errorf("wrapped: %w", ErrInvalidInput)) {
		t.Fatal("IsInvalidInput() = false, want true")
	}
	if !IsConflict(fmt.Errorf("wrapped: %w", ErrConflict)) {
		t.Fatal("IsConflict() = false, want true")
	}
	if !IsTransient(fmt.Errorf("wrapped: %w", ErrTransient)) {
		t.Fatal("IsTransient() = false, want true")
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "conflict", err: ErrConflict, want: true},
		{name: "transient", err: ErrTransient, want: true},
		{name: "not found", err: ErrNotFound, want: false},
		{name: "unauthorized", err: ErrUnauthorized, want: false},
		{name: "invalid input", err: ErrInvalidInput, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryable(tt.err); got != tt.want {
				t.Fatalf("IsRetryable() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestFindSingle(t *testing.T) {
	t.Run("returns ErrNotFound when empty", func(t *testing.T) {
		_, err := findSingle([]string{}, "server", "servers")
		if !IsNotFound(err) {
			t.Fatalf("findSingle() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("returns ErrConflict when multiple", func(t *testing.T) {
		_, err := findSingle([]string{"a", "b"}, "server", "servers")
		if !IsConflict(err) {
			t.Fatalf("findSingle() error = %v, want ErrConflict", err)
		}
	})

	t.Run("returns single element", func(t *testing.T) {
		got, err := findSingle([]string{"a"}, "server", "servers")
		if err != nil {
			t.Fatalf("findSingle() error = %v", err)
		}
		if got != "a" {
			t.Fatalf("findSingle() = %q, want %q", got, "a")
		}
	})
}

func TestFindByTags(t *testing.T) {
	ctx := context.Background()
	tags := map[string]string{"cluster": "test"}

	t.Run("propagates error from listByTags", func(t *testing.T) {
		_, err := FindByTags(ctx, tags, func(context.Context, map[string]string) ([]string, error) {
			return nil, ErrTransient
		}, "server", "servers")
		if !IsTransient(err) {
			t.Fatalf("FindByTags() error = %v, want ErrTransient", err)
		}
	})

	t.Run("returns ErrNotFound when empty", func(t *testing.T) {
		_, err := FindByTags(ctx, tags, func(context.Context, map[string]string) ([]string, error) {
			return nil, nil
		}, "server", "servers")
		if !IsNotFound(err) {
			t.Fatalf("FindByTags() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("returns ErrConflict when multiple", func(t *testing.T) {
		_, err := FindByTags(ctx, tags, func(context.Context, map[string]string) ([]string, error) {
			return []string{"a", "b"}, nil
		}, "server", "servers")
		if !IsConflict(err) {
			t.Fatalf("FindByTags() error = %v, want ErrConflict", err)
		}
	})

	t.Run("returns single element", func(t *testing.T) {
		got, err := FindByTags(ctx, tags, func(context.Context, map[string]string) ([]string, error) {
			return []string{"a"}, nil
		}, "server", "servers")
		if err != nil {
			t.Fatalf("FindByTags() error = %v", err)
		}
		if got != "a" {
			t.Fatalf("FindByTags() = %q, want %q", got, "a")
		}
	})
}

func TestListIDsByTags(t *testing.T) {
	ctx := context.Background()
	tags := map[string]string{"cluster": "test"}
	type item struct{ id string }

	t.Run("propagates error from listByTags", func(t *testing.T) {
		_, err := ListIDsByTags(ctx, tags, func(context.Context, map[string]string) ([]item, error) {
			return nil, ErrTransient
		}, func(i item) string { return i.id })
		if !IsTransient(err) {
			t.Fatalf("ListIDsByTags() error = %v, want ErrTransient", err)
		}
	})

	t.Run("collects non-empty IDs", func(t *testing.T) {
		ids, err := ListIDsByTags(ctx, tags, func(context.Context, map[string]string) ([]item, error) {
			return []item{{id: "id-1"}, {id: ""}, {id: "id-2"}}, nil
		}, func(i item) string { return i.id })
		if err != nil {
			t.Fatalf("ListIDsByTags() error = %v", err)
		}
		if len(ids) != 2 || ids[0] != "id-1" || ids[1] != "id-2" {
			t.Fatalf("ListIDsByTags() = %v, want [id-1 id-2]", ids)
		}
	})
}

type fakeLabeledItem struct {
	id     string
	labels map[string]any
}

func (f *fakeLabeledItem) GetLabels() map[string]any {
	return f.labels
}

func TestListByTags(t *testing.T) {
	tags := map[string]string{"cluster": "c1", "role": "bastion"}

	t.Run("returns ErrInvalidInput when tags are empty", func(t *testing.T) {
		listCalled := false
		_, err := ListByTags(nil, func(string) ([]fakeLabeledItem, error) {
			listCalled = true
			return nil, nil
		}, func(item *fakeLabeledItem) string { return item.id })
		if !IsInvalidInput(err) {
			t.Fatalf("ListByTags() error = %v, want ErrInvalidInput", err)
		}
		if listCalled {
			t.Fatal("list should not be called when tags are empty")
		}
	})

	t.Run("propagates error from list", func(t *testing.T) {
		_, err := ListByTags(tags, func(string) ([]fakeLabeledItem, error) {
			return nil, ErrTransient
		}, func(item *fakeLabeledItem) string { return item.id })
		if !IsTransient(err) {
			t.Fatalf("ListByTags() error = %v, want ErrTransient", err)
		}
	})

	t.Run("passes label selector and filters items matching all tags", func(t *testing.T) {
		var gotSelector string
		got, err := ListByTags(tags, func(selector string) ([]fakeLabeledItem, error) {
			gotSelector = selector
			return []fakeLabeledItem{
				{id: "match", labels: map[string]any{"cluster": "c1", "role": "bastion", "extra": "ok"}},
				{id: "partial", labels: map[string]any{"cluster": "c1"}},
				{id: "other", labels: map[string]any{"cluster": "c2", "role": "bastion"}},
			}, nil
		}, func(item *fakeLabeledItem) string { return item.id })
		if err != nil {
			t.Fatalf("ListByTags() error = %v", err)
		}
		if gotSelector != "cluster=c1,role=bastion" && gotSelector != "role=bastion,cluster=c1" {
			t.Fatalf("selector = %q, want both tag pairs", gotSelector)
		}
		if len(got) != 1 || got[0] != "match" {
			t.Fatalf("ListByTags() = %v, want [match]", got)
		}
	})
}

func TestResolveExisting(t *testing.T) {
	const (
		testID1    = "id-1"
		testByTags = "by-tags"
	)
	ctx := context.Background()
	tags := map[string]string{"cluster": "test"}

	t.Run("returns item by ID without calling findByTags", func(t *testing.T) {
		findByTagsCalled := false
		got, err := ResolveExisting(
			ctx,
			testID1,
			tags,
			func(_ context.Context, id string) (string, error) {
				return "found-" + id, nil
			},
			func(_ context.Context, _ map[string]string) (string, error) {
				findByTagsCalled = true
				return testByTags, nil
			},
			IsNotFound,
		)
		if err != nil {
			t.Fatalf("ResolveExisting() error = %v", err)
		}
		if got != "found-id-1" {
			t.Fatalf("ResolveExisting() = %q, want %q", got, "found-id-1")
		}
		if findByTagsCalled {
			t.Fatal("findByTags should not be called when getByID succeeds")
		}
	})

	t.Run("falls back to findByTags when getByID returns NotFound", func(t *testing.T) {
		got, err := ResolveExisting(
			ctx,
			"stale-id",
			tags,
			func(_ context.Context, _ string) (string, error) {
				return "", ErrNotFound
			},
			func(_ context.Context, _ map[string]string) (string, error) {
				return testByTags, nil
			},
			IsNotFound,
		)
		if err != nil {
			t.Fatalf("ResolveExisting() error = %v", err)
		}
		if got != testByTags {
			t.Fatalf("ResolveExisting() = %q, want %q", got, testByTags)
		}
	})

	t.Run("returns zero value and nil error when both ID and tag lookup return NotFound", func(t *testing.T) {
		got, err := ResolveExisting(
			ctx,
			"stale-id",
			tags,
			func(_ context.Context, _ string) (string, error) {
				return "", ErrNotFound
			},
			func(_ context.Context, _ map[string]string) (string, error) {
				return "", ErrNotFound
			},
			IsNotFound,
		)
		if err != nil {
			t.Fatalf("ResolveExisting() error = %v, want nil", err)
		}
		if got != "" {
			t.Fatalf("ResolveExisting() = %q, want empty", got)
		}
	})

	t.Run("propagates non-ignorable error from getByID", func(t *testing.T) {
		_, err := ResolveExisting(
			ctx,
			testID1,
			tags,
			func(_ context.Context, _ string) (string, error) {
				return "", ErrTransient
			},
			func(_ context.Context, _ map[string]string) (string, error) {
				return testByTags, nil
			},
			IsNotFound,
		)
		if !IsTransient(err) {
			t.Fatalf("ResolveExisting() error = %v, want ErrTransient", err)
		}
	})
}

func TestDeleteByIDOrTags(t *testing.T) {
	const testID1 = "id-1"
	ctx := context.Background()
	tags := map[string]string{"cluster": "test"}

	t.Run("deletes by ID when found without listing by tags", func(t *testing.T) {
		listCalled := false
		deletedIDs, err := DeleteByIDOrTags(
			ctx,
			testID1,
			tags,
			func(_ context.Context, id string) (bool, error) {
				return id == testID1, nil
			},
			func(_ context.Context, _ map[string]string) ([]string, error) {
				listCalled = true
				return []string{"id-2"}, nil
			},
		)
		if err != nil {
			t.Fatalf("DeleteByIDOrTags() error = %v", err)
		}
		if len(deletedIDs) != 1 || deletedIDs[0] != testID1 {
			t.Fatalf("DeleteByIDOrTags() = %v, want [id-1]", deletedIDs)
		}
		if listCalled {
			t.Fatal("listIDsByTags should not be called when deleteByID deletes spec.ID")
		}
	})

	t.Run("falls back to tag lookup when spec.ID is already absent", func(t *testing.T) {
		deletedIDs, err := DeleteByIDOrTags(
			ctx,
			"stale-id",
			tags,
			func(_ context.Context, id string) (bool, error) {
				return id != "stale-id", nil
			},
			func(_ context.Context, _ map[string]string) ([]string, error) {
				return []string{testID1, "id-2"}, nil
			},
		)
		if err != nil {
			t.Fatalf("DeleteByIDOrTags() error = %v", err)
		}
		if len(deletedIDs) != 2 || deletedIDs[0] != testID1 || deletedIDs[1] != "id-2" {
			t.Fatalf("DeleteByIDOrTags() = %v, want [id-1 id-2]", deletedIDs)
		}
	})

	t.Run("continues deleting remaining tag matches on partial error", func(t *testing.T) {
		deletedIDs, err := DeleteByIDOrTags(
			ctx,
			"",
			tags,
			func(_ context.Context, id string) (bool, error) {
				if id == "bad-id" {
					return false, ErrTransient
				}
				return true, nil
			},
			func(_ context.Context, _ map[string]string) ([]string, error) {
				return []string{"bad-id", "good-id"}, nil
			},
		)
		if !IsTransient(err) {
			t.Fatalf("DeleteByIDOrTags() error = %v, want ErrTransient", err)
		}
		if len(deletedIDs) != 1 || deletedIDs[0] != "good-id" {
			t.Fatalf("DeleteByIDOrTags() = %v, want [good-id]", deletedIDs)
		}
	})
}
