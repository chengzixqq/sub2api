export default {
  workspaces: {
    title: '工作区',
    description: '把上游账号与代理按供应商分区，授权分组并约定结算倍率',
    usageHint:
      '启用步骤：① 新建工作区并勾选权限档 → ② 用「分组授权」把分组分给它、约定结算倍率 → ③ 用「成员」搜索并绑定对方账号。绑定后对方用原账号登录，后台会自动切换为只看本工作区的受限视图。',
    createTitle: '新建工作区',
    editTitle: '编辑工作区',
    create: '新建工作区',
    empty: '暂无工作区',
    emptyHint: '新建工作区后，可将供应商用户绑定进来并授权分组',
    description_field: '描述',
    nameRequired: '请填写工作区名称',
    loadFailed: '加载工作区失败',
    deleted: '工作区已删除',
    deleteFailed: '删除工作区失败',
    confirmDeleteTitle: '删除工作区',
    confirmDelete: '删除工作区「{name}」后，其成员将立即失去后台访问权限。确认删除？',

    statusActive: '启用',
    statusDisabled: '停用',
    statusHint: '停用后该工作区成员立即无法访问后台，已建资源保持不变',

    columns: {
      name: '名称',
      status: '状态',
      permissions: '权限档'
    },

    noPermissions: '未授予任何权限',

    perms: {
      accountManage: '账号管理',
      accountManageHint: '在本工作区内新建、编辑、删除上游账号',
      groupOps: '分组运维',
      groupOpsHint: '调整已授权分组的账号绑定与调度参数',
      groupBilling: '分组计费',
      groupBillingHint: '在站长设定的上限内自助下调结算倍率',
      proxyManage: '代理管理',
      proxyManageHint: '管理本工作区自建的代理配置',
      monitorView: '监控查看',
      monitorViewHint: '查看本工作区账号的用量与健康状态'
    },

    members: '成员',
    membersTitle: '「{name}」的成员',
    addMember: '绑定',
    removeMember: '解绑',
    noMembers: '暂无成员',
    memberAdded: '成员已绑定',
    memberAddFailed: '绑定成员失败',
    memberRemoved: '成员已解绑',
    userIdLabel: '用户 #{id}',
    memberSearchPlaceholder: '搜索用户名或邮箱',
    memberSearchEmpty: '没有匹配的用户',
    memberSearchHint: '普通用户绑定进来时会自动设为供应商。站长账号不可绑定。',
    memberSearchVendorOnly: '只看供应商',
    memberAddPromoted: '已绑定「{name}」，并将其角色设为供应商',
    memberWillPromote: '将设为供应商',
    memberAlreadyBound: '已在本工作区',
    memberRoleMismatch: '角色已非供应商，无法访问后台',
    memberRoleAdmin: '站长',
    memberRoleVendor: '供应商',
    memberRoleUser: '普通用户',

    // 站长设定的可调区间。倍率本身挂在账号上，这里只定边界。
    settlementRange: {
      label: '结算倍率可调区间',
      minPlaceholder: '下限（留空不限）',
      maxPlaceholder: '上限（留空不限）',
      hint: '对该工作区名下所有账号统一生效。两端都留空则对方不可自助调价。',
      negative: '结算倍率区间不能为负数',
      inverted: '下限不能高于上限'
    },

    grants: '分组授权',
    grantsTitle: '「{name}」的分组授权',

    grant: {
      group: '分组',
      basePriority: '基础优先级',
      enabled: '启用该授权',
      save: '保存授权',
      none: '暂无分组授权'
    },

    // 供应商自助调价。改的就是账号自身的倍率（计费按账号取值），
    // 可调区间挂在工作区上，对名下所有账号统一生效。
    settlement: {
      entry: '结算倍率',
      title: '结算倍率',
      intro: '这里改的是该账号的结算价。可调区间由站长设定，对你名下所有账号统一生效。',
      maxHint: '上限 {max}x',
      rangeHint: '可调区间 {min}x ~ {max}x',
      notAdjustable: '站长未开放自助调价，如需调整请联系站长。',
      negative: '结算倍率不能为负数',
      loadFailed: '结算信息加载失败',
      saved: '结算倍率已更新',
      exceedsMax: '不得高于站长设定的上限 {max}x',
      belowMin: '不得低于站长设定的下限 {min}x'
    }
  },

  // 结算总览页（供应商侧）。与 workspaces.settlement 的区别是视角：
  // 那边从单个账号出发，这边一次列出全部授权分组，用于对账。
  settlement: {
    title: '用量与结算',
    subtitle: '你的结算倍率可调区间与已授权分组。金额一律按成本口径呈现。',
    workspaceLabel: '所属工作区',
    rangeLabel: '结算倍率可调区间',
    rangeValue: '{min}x ~ {max}x',
    rangeHint: '倍率按账号设置，入口在「账号管理」中每个账号的「结算倍率」。',
    noGrants: '暂无分组授权，请联系站长开通。',
    ownerNotApplicable: '本页面面向供应商。站长请在「工作区」页管理各工作区的结算区间与分组授权。',
    noCeiling: '站长未开放自助调价',
    enabled: '生效中',
    disabled: '已停用',
    loadFailed: '结算信息加载失败',
    columns: {
      group: '分组',
      priority: '基准优先级',
      status: '状态'
    }
  }
}
