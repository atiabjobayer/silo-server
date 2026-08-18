package auth

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestNormalizePermissions_DeduplicatesAndSorts(t *testing.T) {
	got, err := NormalizePermissions([]string{
		"marker_edit",
		" metadata_curation ",
		"metadata_curation",
		"",
	})
	if err != nil {
		t.Fatalf("NormalizePermissions returned error: %v", err)
	}
	want := []string{"marker_edit", "metadata_curation"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions = %#v, want %#v", got, want)
	}
}

func TestNormalizePermissions_RejectsUnknownPermission(t *testing.T) {
	if _, err := NormalizePermissions([]string{"server_owner"}); err == nil {
		t.Fatal("expected unknown permission error")
	}
}

func TestNormalizePermissions_AcceptsFeatureAccessPermissions(t *testing.T) {
	got, err := NormalizePermissions([]string{
		"watch_party",
		"settings_appearance",
		"settings_home_screen",
		"settings_libraries",
	})
	if err != nil {
		t.Fatalf("NormalizePermissions returned error: %v", err)
	}
	want := []string{"settings_appearance", "settings_home_screen", "settings_libraries", "watch_party"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions = %#v, want %#v", got, want)
	}
}

func TestHasEffectivePermission_AdminImpliesAssignablePermissions(t *testing.T) {
	user := &models.User{Role: "admin", Enabled: true}
	if !HasEffectivePermission(user, PermissionMetadataCuration) {
		t.Fatal("admin should have metadata curation")
	}
	if !HasEffectivePermission(user, PermissionMarkerEdit) {
		t.Fatal("admin should have marker edit")
	}
}

func TestHasEffectivePermission_UserRequiresAssignedPermission(t *testing.T) {
	user := &models.User{Role: "user", Enabled: true}
	if HasEffectivePermission(user, PermissionMetadataCuration) {
		t.Fatal("plain user should not have metadata curation")
	}
	user.Permissions = []string{"metadata_curation"}
	if !HasEffectivePermission(user, PermissionMetadataCuration) {
		t.Fatal("assigned user should have metadata curation")
	}
}

func TestHasEffectivePermission_FeatureAccessPermissionsFollowAdminAndAssignedRules(t *testing.T) {
	for _, permission := range []Permission{
		PermissionWatchParty,
		PermissionSettingsAppearance,
		PermissionSettingsHomeScreen,
		PermissionSettingsLibraries,
	} {
		admin := &models.User{Role: "admin", Enabled: true}
		if !HasEffectivePermission(admin, permission) {
			t.Fatalf("admin should have %s", permission)
		}

		plain := &models.User{Role: "user", Enabled: true}
		if HasEffectivePermission(plain, permission) {
			t.Fatalf("plain user should not have %s by default", permission)
		}
		plain.Permissions = []string{string(permission)}
		if !HasEffectivePermission(plain, permission) {
			t.Fatalf("assigned user should have %s", permission)
		}
	}
}

func TestDefaultUserPermissionsIncludesMarkerEditOnly(t *testing.T) {
	got := DefaultUserPermissions()
	want := []string{"marker_edit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default permissions = %#v, want %#v", got, want)
	}
}
