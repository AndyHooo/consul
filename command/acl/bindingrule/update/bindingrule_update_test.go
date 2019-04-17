package bindingruleupdate

import (
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/consul/agent"
	"github.com/hashicorp/consul/agent/connect"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/command/acl"
	"github.com/hashicorp/consul/logger"
	"github.com/hashicorp/consul/sdk/testutil"
	"github.com/hashicorp/consul/testrpc"
	"github.com/mitchellh/cli"
	"github.com/stretchr/testify/require"

	uuid "github.com/hashicorp/go-uuid"
)

func TestBindingRuleUpdateCommand_noTabs(t *testing.T) {
	t.Parallel()

	if strings.ContainsRune(New(cli.NewMockUi()).Help(), '\t') {
		t.Fatal("help has tabs")
	}
}

func TestBindingRuleUpdateCommand(t *testing.T) {
	t.Parallel()

	testDir := testutil.TempDir(t, "acl")
	defer os.RemoveAll(testDir)

	a := agent.NewTestAgent(t, t.Name(), `
	primary_datacenter = "dc1"
	acl {
		enabled = true
		tokens {
			master = "root"
		}
	}`)

	a.Agent.LogWriter = logger.NewLogWriter(512)

	defer a.Shutdown()
	testrpc.WaitForLeader(t, a.RPC, "dc1")

	client := a.Client()

	// create an idp in advance
	{
		ca := connect.TestCA(t, nil)
		_, _, err := client.ACL().IdentityProviderCreate(
			&api.ACLIdentityProvider{
				Name:                        "k8s",
				Type:                        "kubernetes",
				KubernetesHost:              "https://foo.internal:8443",
				KubernetesCACert:            ca.RootCert,
				KubernetesServiceAccountJWT: acl.TestKubernetesJWT_A,
			},
			&api.WriteOptions{Token: "root"},
		)
		require.NoError(t, err)
	}

	deleteRules := func(t *testing.T) {
		rules, _, err := client.ACL().BindingRuleList(
			"k8s",
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)

		for _, rule := range rules {
			_, err := client.ACL().BindingRuleDelete(
				rule.ID,
				&api.WriteOptions{Token: "root"},
			)
			require.NoError(t, err)
		}
	}

	t.Run("rule id required", func(t *testing.T) {
		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
		}

		ui := cli.NewMockUi()
		cmd := New(ui)

		code := cmd.Run(args)
		require.Equal(t, code, 1)
		require.Contains(t, ui.ErrorWriter.String(), "Cannot update a binding rule without specifying the -id parameter")
	})

	t.Run("rule id partial matches nothing", func(t *testing.T) {
		fakeID, err := uuid.GenerateUUID()
		require.NoError(t, err)

		ui := cli.NewMockUi()
		cmd := New(ui)
		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-id=" + fakeID[0:5],
			"-token=root",
			"-description=test rule edited",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 1)
		require.Contains(t, ui.ErrorWriter.String(), "Error determining binding rule ID")
	})

	t.Run("rule id exact match doesn't exist", func(t *testing.T) {
		fakeID, err := uuid.GenerateUUID()
		require.NoError(t, err)

		ui := cli.NewMockUi()
		cmd := New(ui)
		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-id=" + fakeID,
			"-token=root",
			"-description=test rule edited",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 1)
		require.Contains(t, ui.ErrorWriter.String(), "Binding rule not found with ID")
	})

	createRule := func(t *testing.T) string {
		rule, _, err := client.ACL().BindingRuleCreate(
			&api.ACLBindingRule{
				IDPName:      "k8s",
				Description:  "test rule",
				RoleBindType: api.BindingRuleRoleBindTypeService,
				RoleName:     "k8s-{{serviceaccount.name}}",
				Selector:     "serviceaccount.namespace==default",
			},
			&api.WriteOptions{Token: "root"},
		)
		require.NoError(t, err)
		return rule.ID
	}

	createDupe := func(t *testing.T) string {
		for {
			// Check for 1-char duplicates.
			rules, _, err := client.ACL().BindingRuleList(
				"k8s",
				&api.QueryOptions{Token: "root"},
			)
			require.NoError(t, err)

			m := make(map[byte]struct{})
			for _, rule := range rules {
				c := rule.ID[0]

				if _, ok := m[c]; ok {
					return string(c)
				}
				m[c] = struct{}{}
			}

			_ = createRule(t)
		}
	}

	t.Run("rule id partial matches multiple", func(t *testing.T) {
		prefix := createDupe(t)

		ui := cli.NewMockUi()
		cmd := New(ui)
		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-id=" + prefix,
			"-token=root",
			"-description=test rule edited",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 1)
		require.Contains(t, ui.ErrorWriter.String(), "Error determining binding rule ID")
	})

	t.Run("must use roughly valid selector", func(t *testing.T) {
		id := createRule(t)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-id", id,
			"-selector", "foo",
		}

		ui := cli.NewMockUi()
		cmd := New(ui)

		code := cmd.Run(args)
		require.Equal(t, code, 1)
		require.Contains(t, ui.ErrorWriter.String(), "Selector is invalid")
	})

	t.Run("update all fields", func(t *testing.T) {
		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-id", id,
			"-description=test rule edited",
			"-role-bind-type", "existing",
			"-role-name=role-updated",
			"-selector=serviceaccount.namespace==alt and serviceaccount.name==demo",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 0, "err: %s", ui.ErrorWriter.String())
		require.Empty(t, ui.ErrorWriter.String())

		rule, _, err := client.ACL().BindingRuleRead(
			id,
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)
		require.NotNil(t, rule)

		require.Equal(t, "test rule edited", rule.Description)
		require.Equal(t, "role-updated", rule.RoleName)
		require.Equal(t, api.BindingRuleRoleBindTypeExisting, rule.RoleBindType)
		require.Equal(t, "serviceaccount.namespace==alt and serviceaccount.name==demo", rule.Selector)
	})

	t.Run("update all fields - partial", func(t *testing.T) {
		deleteRules(t) // reset since we created a bunch that might be dupes

		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-id", id[0:5],
			"-description=test rule edited",
			"-role-bind-type", "existing",
			"-role-name=role-updated",
			"-selector=serviceaccount.namespace==alt and serviceaccount.name==demo",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 0, "err: %s", ui.ErrorWriter.String())
		require.Empty(t, ui.ErrorWriter.String())

		rule, _, err := client.ACL().BindingRuleRead(
			id,
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)
		require.NotNil(t, rule)

		require.Equal(t, "test rule edited", rule.Description)
		require.Equal(t, "role-updated", rule.RoleName)
		require.Equal(t, api.BindingRuleRoleBindTypeExisting, rule.RoleBindType)
		require.Equal(t, "serviceaccount.namespace==alt and serviceaccount.name==demo", rule.Selector)
	})

	t.Run("update all fields but description", func(t *testing.T) {
		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-id", id,
			"-role-bind-type", "existing",
			"-role-name=role-updated",
			"-selector=serviceaccount.namespace==alt and serviceaccount.name==demo",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 0, "err: %s", ui.ErrorWriter.String())
		require.Empty(t, ui.ErrorWriter.String())

		rule, _, err := client.ACL().BindingRuleRead(
			id,
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)
		require.NotNil(t, rule)

		require.Equal(t, "test rule", rule.Description)
		require.Equal(t, "role-updated", rule.RoleName)
		require.Equal(t, api.BindingRuleRoleBindTypeExisting, rule.RoleBindType)
		require.Equal(t, "serviceaccount.namespace==alt and serviceaccount.name==demo", rule.Selector)
	})

	t.Run("update all fields but role name", func(t *testing.T) {
		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-id", id,
			"-description=test rule edited",
			"-role-bind-type", "existing",
			"-selector=serviceaccount.namespace==alt and serviceaccount.name==demo",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 0, "err: %s", ui.ErrorWriter.String())
		require.Empty(t, ui.ErrorWriter.String())

		rule, _, err := client.ACL().BindingRuleRead(
			id,
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)
		require.NotNil(t, rule)

		require.Equal(t, "test rule edited", rule.Description)
		require.Equal(t, "k8s-{{serviceaccount.name}}", rule.RoleName)
		require.Equal(t, api.BindingRuleRoleBindTypeExisting, rule.RoleBindType)
		require.Equal(t, "serviceaccount.namespace==alt and serviceaccount.name==demo", rule.Selector)
	})

	t.Run("update all fields but must exist", func(t *testing.T) {
		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-id", id,
			"-description=test rule edited",
			"-role-name=role-updated",
			"-selector=serviceaccount.namespace==alt and serviceaccount.name==demo",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 0, "err: %s", ui.ErrorWriter.String())
		require.Empty(t, ui.ErrorWriter.String())

		rule, _, err := client.ACL().BindingRuleRead(
			id,
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)
		require.NotNil(t, rule)

		require.Equal(t, "test rule edited", rule.Description)
		require.Equal(t, "role-updated", rule.RoleName)
		require.Equal(t, api.BindingRuleRoleBindTypeService, rule.RoleBindType)
		require.Equal(t, "serviceaccount.namespace==alt and serviceaccount.name==demo", rule.Selector)
	})

	t.Run("update all fields but selector", func(t *testing.T) {
		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-id", id,
			"-description=test rule edited",
			"-role-bind-type", "existing",
			"-role-name=role-updated",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 0, "err: %s", ui.ErrorWriter.String())
		require.Empty(t, ui.ErrorWriter.String())

		rule, _, err := client.ACL().BindingRuleRead(
			id,
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)
		require.NotNil(t, rule)

		require.Equal(t, "test rule edited", rule.Description)
		require.Equal(t, "role-updated", rule.RoleName)
		require.Equal(t, api.BindingRuleRoleBindTypeExisting, rule.RoleBindType)
		require.Equal(t, "serviceaccount.namespace==default", rule.Selector)
	})

	t.Run("update all fields clear selector", func(t *testing.T) {
		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-id", id,
			"-description=test rule edited",
			"-role-bind-type", "existing",
			"-role-name=role-updated",
			"-selector=",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 0, "err: %s", ui.ErrorWriter.String())
		require.Empty(t, ui.ErrorWriter.String())

		rule, _, err := client.ACL().BindingRuleRead(
			id,
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)
		require.NotNil(t, rule)

		require.Equal(t, "test rule edited", rule.Description)
		require.Equal(t, "role-updated", rule.RoleName)
		require.Equal(t, api.BindingRuleRoleBindTypeExisting, rule.RoleBindType)
		require.Empty(t, rule.Selector)
	})
}

func TestBindingRuleUpdateCommand_noMerge(t *testing.T) {
	t.Parallel()

	testDir := testutil.TempDir(t, "acl")
	defer os.RemoveAll(testDir)

	a := agent.NewTestAgent(t, t.Name(), `
	primary_datacenter = "dc1"
	acl {
		enabled = true
		tokens {
			master = "root"
		}
	}`)

	a.Agent.LogWriter = logger.NewLogWriter(512)

	defer a.Shutdown()
	testrpc.WaitForLeader(t, a.RPC, "dc1")

	client := a.Client()

	// create an idp in advance
	{
		ca := connect.TestCA(t, nil)
		_, _, err := client.ACL().IdentityProviderCreate(
			&api.ACLIdentityProvider{
				Name:                        "k8s",
				Type:                        "kubernetes",
				KubernetesHost:              "https://foo.internal:8443",
				KubernetesCACert:            ca.RootCert,
				KubernetesServiceAccountJWT: acl.TestKubernetesJWT_A,
			},
			&api.WriteOptions{Token: "root"},
		)
		require.NoError(t, err)
	}

	deleteRules := func(t *testing.T) {
		rules, _, err := client.ACL().BindingRuleList(
			"k8s",
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)

		for _, rule := range rules {
			_, err := client.ACL().BindingRuleDelete(
				rule.ID,
				&api.WriteOptions{Token: "root"},
			)
			require.NoError(t, err)
		}
	}

	t.Run("rule id required", func(t *testing.T) {
		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-no-merge",
		}

		ui := cli.NewMockUi()
		cmd := New(ui)

		code := cmd.Run(args)
		require.Equal(t, code, 1)
		require.Contains(t, ui.ErrorWriter.String(), "Cannot update a binding rule without specifying the -id parameter")
	})

	t.Run("rule id partial matches nothing", func(t *testing.T) {
		fakeID, err := uuid.GenerateUUID()
		require.NoError(t, err)

		ui := cli.NewMockUi()
		cmd := New(ui)
		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-id=" + fakeID[0:5],
			"-token=root",
			"-no-merge",
			"-description=test rule edited",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 1)
		require.Contains(t, ui.ErrorWriter.String(), "Error determining binding rule ID")
	})

	t.Run("rule id exact match doesn't exist", func(t *testing.T) {
		fakeID, err := uuid.GenerateUUID()
		require.NoError(t, err)

		ui := cli.NewMockUi()
		cmd := New(ui)
		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-id=" + fakeID,
			"-token=root",
			"-no-merge",
			"-description=test rule edited",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 1)
		require.Contains(t, ui.ErrorWriter.String(), "Binding rule not found with ID")
	})

	createRule := func(t *testing.T) string {
		rule, _, err := client.ACL().BindingRuleCreate(
			&api.ACLBindingRule{
				IDPName:      "k8s",
				Description:  "test rule",
				RoleBindType: api.BindingRuleRoleBindTypeExisting,
				RoleName:     "k8s-{{serviceaccount.name}}",
				Selector:     "serviceaccount.namespace==default",
			},
			&api.WriteOptions{Token: "root"},
		)
		require.NoError(t, err)
		return rule.ID
	}

	createDupe := func(t *testing.T) string {
		for {
			// Check for 1-char duplicates.
			rules, _, err := client.ACL().BindingRuleList(
				"k8s",
				&api.QueryOptions{Token: "root"},
			)
			require.NoError(t, err)

			m := make(map[byte]struct{})
			for _, rule := range rules {
				c := rule.ID[0]

				if _, ok := m[c]; ok {
					return string(c)
				}
				m[c] = struct{}{}
			}

			_ = createRule(t)
		}
	}

	t.Run("rule id partial matches multiple", func(t *testing.T) {
		prefix := createDupe(t)

		ui := cli.NewMockUi()
		cmd := New(ui)
		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-id=" + prefix,
			"-token=root",
			"-no-merge",
			"-description=test rule edited",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 1)
		require.Contains(t, ui.ErrorWriter.String(), "Error determining binding rule ID")
	})

	t.Run("must use roughly valid selector", func(t *testing.T) {
		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-no-merge",
			"-id", id,
			"-description=test rule edited",
			"-role-bind-type", "service",
			"-role-name=role-updated",
			"-selector", "foo",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 1)
		require.Contains(t, ui.ErrorWriter.String(), "Selector is invalid")
	})

	t.Run("update all fields", func(t *testing.T) {
		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-no-merge",
			"-id", id,
			"-description=test rule edited",
			"-role-bind-type", "service",
			"-role-name=role-updated",
			"-selector=serviceaccount.namespace==alt and serviceaccount.name==demo",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 0, "err: %s", ui.ErrorWriter.String())
		require.Empty(t, ui.ErrorWriter.String())

		rule, _, err := client.ACL().BindingRuleRead(
			id,
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)
		require.NotNil(t, rule)

		require.Equal(t, "test rule edited", rule.Description)
		require.Equal(t, "role-updated", rule.RoleName)
		require.Equal(t, api.BindingRuleRoleBindTypeService, rule.RoleBindType)
		require.Equal(t, "serviceaccount.namespace==alt and serviceaccount.name==demo", rule.Selector)
	})

	t.Run("update all fields - partial", func(t *testing.T) {
		deleteRules(t) // reset since we created a bunch that might be dupes

		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-no-merge",
			"-id", id[0:5],
			"-description=test rule edited",
			"-role-bind-type", "service",
			"-role-name=role-updated",
			"-selector=serviceaccount.namespace==alt and serviceaccount.name==demo",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 0, "err: %s", ui.ErrorWriter.String())
		require.Empty(t, ui.ErrorWriter.String())

		rule, _, err := client.ACL().BindingRuleRead(
			id,
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)
		require.NotNil(t, rule)

		require.Equal(t, "test rule edited", rule.Description)
		require.Equal(t, "role-updated", rule.RoleName)
		require.Equal(t, api.BindingRuleRoleBindTypeService, rule.RoleBindType)
		require.Equal(t, "serviceaccount.namespace==alt and serviceaccount.name==demo", rule.Selector)
	})

	t.Run("update all fields but description", func(t *testing.T) {
		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-no-merge",
			"-id", id,
			"-role-bind-type", "service",
			"-role-name=role-updated",
			"-selector=serviceaccount.namespace==alt and serviceaccount.name==demo",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 0, "err: %s", ui.ErrorWriter.String())
		require.Empty(t, ui.ErrorWriter.String())

		rule, _, err := client.ACL().BindingRuleRead(
			id,
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)
		require.NotNil(t, rule)

		require.Empty(t, rule.Description)
		require.Equal(t, "role-updated", rule.RoleName)
		require.Equal(t, api.BindingRuleRoleBindTypeService, rule.RoleBindType)
		require.Equal(t, "serviceaccount.namespace==alt and serviceaccount.name==demo", rule.Selector)
	})

	t.Run("missing role name", func(t *testing.T) {
		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)
		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-no-merge",
			"-id=" + id,
			"-description=test rule edited",
			"-role-bind-type", "existing",
			"-selector=serviceaccount.namespace==alt and serviceaccount.name==demo",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 1)
		require.Contains(t, ui.ErrorWriter.String(), "Missing required '-role-name' flag")
	})

	t.Run("update all fields but must exist", func(t *testing.T) {
		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-no-merge",
			"-id", id,
			"-description=test rule edited",
			"-role-name=role-updated",
			"-selector=serviceaccount.namespace==alt and serviceaccount.name==demo",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 0, "err: %s", ui.ErrorWriter.String())
		require.Empty(t, ui.ErrorWriter.String())

		rule, _, err := client.ACL().BindingRuleRead(
			id,
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)
		require.NotNil(t, rule)

		require.Equal(t, "test rule edited", rule.Description)
		require.Equal(t, "role-updated", rule.RoleName)
		require.Equal(t, api.BindingRuleRoleBindTypeService, rule.RoleBindType) // reset to zero value
		require.Equal(t, "serviceaccount.namespace==alt and serviceaccount.name==demo", rule.Selector)
	})

	t.Run("update all fields but selector", func(t *testing.T) {
		id := createRule(t)

		ui := cli.NewMockUi()
		cmd := New(ui)

		args := []string{
			"-http-addr=" + a.HTTPAddr(),
			"-token=root",
			"-no-merge",
			"-id", id,
			"-description=test rule edited",
			"-role-bind-type", "service",
			"-role-name=role-updated",
		}

		code := cmd.Run(args)
		require.Equal(t, code, 0, "err: %s", ui.ErrorWriter.String())
		require.Empty(t, ui.ErrorWriter.String())

		rule, _, err := client.ACL().BindingRuleRead(
			id,
			&api.QueryOptions{Token: "root"},
		)
		require.NoError(t, err)
		require.NotNil(t, rule)

		require.Equal(t, "test rule edited", rule.Description)
		require.Equal(t, "role-updated", rule.RoleName)
		require.Equal(t, api.BindingRuleRoleBindTypeService, rule.RoleBindType)
		require.Empty(t, rule.Selector)
	})
}