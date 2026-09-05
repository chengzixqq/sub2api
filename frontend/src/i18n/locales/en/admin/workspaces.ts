export default {
  workspaces: {
    title: 'Workspaces',
    description:
      'Partition upstream accounts and proxies per vendor, grant groups and agree on cost rates',
    usageHint:
      'Setup: (1) create a workspace and tick its permissions, (2) use "Group grants" to assign groups and set cost rates, (3) use "Members" to search for and bind the vendor. Once bound, they sign in with their existing account and the admin area switches to a view scoped to this workspace only.',
    createTitle: 'New workspace',
    editTitle: 'Edit workspace',
    create: 'New workspace',
    empty: 'No workspaces yet',
    emptyHint: 'Create a workspace, then bind vendor users to it and grant groups',
    description_field: 'Description',
    nameRequired: 'Workspace name is required',
    loadFailed: 'Failed to load workspaces',
    deleted: 'Workspace deleted',
    deleteFailed: 'Failed to delete workspace',
    confirmDeleteTitle: 'Delete workspace',
    confirmDelete:
      'Deleting workspace "{name}" immediately revokes admin access for its members. Continue?',

    statusActive: 'Active',
    statusDisabled: 'Disabled',
    statusHint:
      'Disabling revokes admin access for its members immediately; existing resources are untouched',

    columns: {
      name: 'Name',
      status: 'Status',
      permissions: 'Permissions'
    },

    noPermissions: 'No permissions granted',

    perms: {
      accountManage: 'Account management',
      accountManageHint: 'Create, edit and delete upstream accounts within this workspace',
      groupOps: 'Group operations',
      groupOpsHint: 'Adjust account bindings and scheduling for granted groups',
      groupBilling: 'Group billing',
      groupBillingHint: 'Lower their own cost rate within the ceiling you set',
      proxyManage: 'Proxy management',
      proxyManageHint: 'Manage proxies created inside this workspace',
      monitorView: 'Monitoring',
      monitorViewHint: 'View usage and health for accounts in this workspace'
    },

    members: 'Members',
    membersTitle: 'Members of "{name}"',
    addMember: 'Bind',
    removeMember: 'Unbind',
    noMembers: 'No members yet',
    memberAdded: 'Member bound',
    memberAddPromoted: 'Member bound and "{name}" is now a vendor',
    memberAddFailed: 'Failed to bind member',
    memberRemoved: 'Member unbound',
    userIdLabel: 'User #{id}',
    memberSearchPlaceholder: 'Search by username or email',
    memberSearchEmpty: 'No matching users',
    memberSearchHint: 'Plain users are promoted to vendor automatically when bound. Owner accounts cannot be bound.',
    memberSearchVendorOnly: 'Vendors only',
    memberWillPromote: 'Will become a vendor',
    memberAlreadyBound: 'Already in this workspace',
    memberRoleMismatch: 'Role is no longer Vendor; cannot access the admin area',
    memberRoleAdmin: 'Owner',
    memberRoleVendor: 'Vendor',
    memberRoleUser: 'Plain user',

    // Owner-defined adjustable range. Rates live on accounts; this only sets bounds.
    settlementRange: {
      label: 'Adjustable cost-rate range',
      minPlaceholder: 'Floor (empty = none)',
      maxPlaceholder: 'Ceiling (empty = none)',
      hint: 'Applies to every account in this workspace. Leave both empty to disallow self-service pricing.',
      negative: 'Cost-rate bounds cannot be negative',
      inverted: 'Floor cannot exceed the ceiling'
    },

    grants: 'Group grants',
    grantsTitle: 'Group grants for "{name}"',

    grant: {
      group: 'Group',
      basePriority: 'Base priority',
      enabled: 'Enable this grant',
      save: 'Save grant',
      none: 'No group grants yet'
    },

    // Vendor self-service pricing. Edits the account's own multiplier (billing reads
    // it per account); the adjustable range lives on the workspace and applies to
    // every account it owns.
    settlement: {
      entry: 'Cost rate',
      title: 'Cost rate',
      intro: 'This sets the account\'s settlement rate. The owner defines the adjustable range, which applies to every account you own.',
      maxHint: 'Ceiling {max}x',
      rangeHint: 'Range {min}x ~ {max}x',
      notAdjustable: 'The owner has not enabled self-service pricing. Contact them to adjust it.',
      negative: 'Cost rate cannot be negative',
      loadFailed: 'Failed to load settlement info',
      saved: 'Cost rate updated',
      exceedsMax: 'Cannot exceed the owner-set ceiling of {max}x',
      belowMin: 'Cannot go below the owner-set floor of {min}x'
    }
  },

  // Vendor-side settlement overview. Differs from workspaces.settlement in vantage
  // point: that one starts from a single account, this lists every granted group
  // at once for reconciliation.
  settlement: {
    title: 'Usage & Settlement',
    subtitle: 'Your adjustable cost-rate range and granted groups. Amounts are shown at cost.',
    workspaceLabel: 'Workspace',
    rangeLabel: 'Adjustable cost-rate range',
    rangeValue: '{min}x ~ {max}x',
    rangeHint: 'Rates are set per account, under "Cost rate" on each account in Accounts.',
    noGrants: 'No group grants yet. Contact the owner to get set up.',
    ownerNotApplicable:
      'This page is for vendors. Owners can manage each workspace\'s rate range and group grants on the Workspaces page.',
    noCeiling: 'Self-service pricing not enabled',
    enabled: 'Active',
    disabled: 'Disabled',
    loadFailed: 'Failed to load settlement info',
    columns: {
      group: 'Group',
      priority: 'Base priority',
      status: 'Status'
    }
  }
}
