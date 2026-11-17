package authz

import "testing"

func TestPermissionOrder(t *testing.T) {
	cases := []struct {
		have, need Permission
		want       bool
	}{
		{Manage, Edit, true},
		{Edit, View, true},
		{View, Edit, false},
		{None, View, false},
		{Edit, Manage, false},
		{View, View, true},
	}
	for _, c := range cases {
		if got := c.have.Allows(c.need); got != c.want {
			t.Errorf("%q.Allows(%q) = %v", c.have, c.need, got)
		}
	}
	if Max(View, Edit) != Edit || Max(Manage, None) != Manage {
		t.Error("Max wrong")
	}
	if Permission("admin").Valid() || None.Valid() || !View.Valid() {
		t.Error("Valid wrong")
	}
}
