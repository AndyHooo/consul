package bindingrulecreate

import (
	"flag"
	"fmt"

	"github.com/hashicorp/consul/api"
	aclhelpers "github.com/hashicorp/consul/command/acl"
	"github.com/hashicorp/consul/command/flags"
	"github.com/mitchellh/cli"
)

func New(ui cli.Ui) *cmd {
	c := &cmd{UI: ui}
	c.init()
	return c
}

type cmd struct {
	UI    cli.Ui
	flags *flag.FlagSet
	http  *flags.HTTPFlags
	help  string

	idpName      string
	description  string
	selector     string
	roleBindType string
	roleName     string

	showMeta bool
}

func (c *cmd) init() {
	c.flags = flag.NewFlagSet("", flag.ContinueOnError)

	c.flags.BoolVar(
		&c.showMeta,
		"meta",
		false,
		"Indicates that binding rule metadata such "+
			"as the content hash and raft indices should be shown for each entry.",
	)

	c.flags.StringVar(
		&c.idpName,
		"idp-name",
		"",
		"The identity provider's name for which this binding rule applies. "+
			"This flag is required.",
	)
	c.flags.StringVar(
		&c.description,
		"description",
		"",
		"A description of the binding rule.",
	)
	c.flags.StringVar(
		&c.selector,
		"selector",
		"",
		"Selector is an expression that matches against verified identity "+
			"attributes returned from the identity provider during login.",
	)
	c.flags.StringVar(
		&c.roleBindType,
		"role-bind-type",
		string(api.BindingRuleRoleBindTypeService),
		"Type of role binding to perform (\"service\" or \"existing\").",
	)
	c.flags.StringVar(
		&c.roleName,
		"role-name",
		"",
		"Name of role to bind on match. Can use {{var}} interpolation. "+
			"This flag is required.",
	)

	c.http = &flags.HTTPFlags{}
	flags.Merge(c.flags, c.http.ClientFlags())
	flags.Merge(c.flags, c.http.ServerFlags())
	c.help = flags.Usage(help, c.flags)
}

func (c *cmd) Run(args []string) int {
	if err := c.flags.Parse(args); err != nil {
		return 1
	}

	if c.idpName == "" {
		c.UI.Error(fmt.Sprintf("Missing required '-idp-name' flag"))
		c.UI.Error(c.Help())
		return 1
	} else if c.roleName == "" {
		c.UI.Error(fmt.Sprintf("Missing required '-role-name' flag"))
		c.UI.Error(c.Help())
		return 1
	}

	newRule := &api.ACLBindingRule{
		Description:  c.description,
		IDPName:      c.idpName,
		RoleBindType: api.BindingRuleRoleBindType(c.roleBindType),
		RoleName:     c.roleName,
		Selector:     c.selector,
	}

	client, err := c.http.APIClient()
	if err != nil {
		c.UI.Error(fmt.Sprintf("Error connecting to Consul agent: %s", err))
		return 1
	}

	rule, _, err := client.ACL().BindingRuleCreate(newRule, nil)
	if err != nil {
		c.UI.Error(fmt.Sprintf("Failed to create new binding rule: %v", err))
		return 1
	}

	aclhelpers.PrintBindingRule(rule, c.UI, c.showMeta)
	return 0
}

func (c *cmd) Synopsis() string {
	return synopsis
}

func (c *cmd) Help() string {
	return flags.Usage(c.help, nil)
}

const synopsis = "Create an ACL Binding Rule"

const help = `
Usage: consul acl binding-rule create [options]

  Create a new binding rule:

     $ consul acl binding-rule create \
            -idp-name=minikube \
            -role-name="k8s-{{serviceaccount.name}}" \
            -selector='serviceaccount.namespace==default and serviceaccount.name==web'
`