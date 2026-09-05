import { apiClient } from '@/api/client'

/**
 * 五档权限开关，与后端 service.WorkspacePermissions 一一对应。
 *
 * 整档提交语义：每次 update 传的是完整状态，省略某档等于关闭它。
 */
export interface WorkspacePermissions {
  account_manage: boolean
  group_ops: boolean
  group_billing: boolean
  proxy_manage: boolean
  monitor_view: boolean
}

export interface Workspace {
  id: number
  name: string
  description: string
  status: 'active' | 'disabled'
  permissions: WorkspacePermissions
  /**
   * 供应商自设账号结算倍率的可调区间，null 表示该端不设限。
   *
   * 与 0 不同：null 是「站长没设这一端」，0 是「站长把这一端设成了 0」。
   * 供应商侧据此决定输入框是否显示边界提示。两端都为 null 时不可自调。
   */
  settlement_rate_min: number | null
  settlement_rate_max: number | null
  created_at: number
  updated_at: number
}

/**
 * 工作区成员。
 *
 * email/username/role 在关联用户已被删除时为空串 —— 绑定行仍会返回，
 * 便于站长发现并解绑这类脏数据。role 非 'vendor' 说明该绑定是历史遗留
 * 或角色被改回，此时对方实际访问不了管理端。
 */
export interface WorkspaceMember {
  id: number
  user_id: number
  workspace_id: number
  email: string
  username: string
  role: string
  created_at: number
  /**
   * 仅出现在绑定响应里：true 表示该用户原为普通用户，本次已顺带提升为
   * 供应商。列表响应中后端省略该字段，故为可选。
   */
  role_promoted?: boolean
}

/**
 * 分组授权：只表达「这个工作区能用这个分组」以及入组时的基准优先级。
 *
 * 不含结算倍率 —— 倍率挂在账号上（Account.rate_multiplier），在工作区的
 * settlement_rate_min/max 区间内由供应商自设。授权表曾带过 cost_rate_*，
 * 那个口径已废弃：同一账号在不同分组下会有不同结算价，对不上账。
 */
export interface WorkspaceGrant {
  id: number
  workspace_id: number
  group_id: number
  base_priority: number
  enabled: boolean
  created_at: number
  updated_at: number
}

export interface CreateWorkspaceRequest {
  name: string
  description?: string
  permissions: WorkspacePermissions
  /** 省略即两端都不设限，供应商不可自调倍率。 */
  settlement_rate_min?: number | null
  settlement_rate_max?: number | null
}

/** 字段省略即不修改；这与后端的指针语义一致。 */
export interface UpdateWorkspaceRequest {
  name?: string
  description?: string
  status?: 'active' | 'disabled'
  permissions?: WorkspacePermissions
  /**
   * 三态：字段省略=不改，传 0=清掉该端边界，传正数=设为该值。
   *
   * 用 0 而非 null 表达「清除」，是因为后端 *float64 分不开 JSON null
   * 与字段缺省，两者都解成 nil。
   */
  settlement_rate_min?: number
  settlement_rate_max?: number
}

export interface UpsertGrantRequest {
  group_id: number
  base_priority?: number
  enabled?: boolean
}

/**
 * vendor 自读的返回：本工作区 + 其全部授权。
 *
 * workspace 为 null 表示当前身份不属于任何工作区（站长），
 * 前端可用这一个请求同时判断身份归属。
 */
export interface MyWorkspace {
  workspace: Workspace | null
  grants: WorkspaceGrant[]
}

export const adminWorkspacesAPI = {
  async list(): Promise<Workspace[]> {
    const { data } = await apiClient.get<Workspace[]>('/admin/workspaces')
    return data
  },

  async get(id: number): Promise<Workspace> {
    const { data } = await apiClient.get<Workspace>(`/admin/workspaces/${id}`)
    return data
  },

  async create(payload: CreateWorkspaceRequest): Promise<Workspace> {
    const { data } = await apiClient.post<Workspace>('/admin/workspaces', payload)
    return data
  },

  async update(id: number, payload: UpdateWorkspaceRequest): Promise<Workspace> {
    const { data } = await apiClient.put<Workspace>(`/admin/workspaces/${id}`, payload)
    return data
  },

  async remove(id: number): Promise<void> {
    await apiClient.delete(`/admin/workspaces/${id}`)
  },

  async listMembers(id: number): Promise<WorkspaceMember[]> {
    const { data } = await apiClient.get<WorkspaceMember[]>(`/admin/workspaces/${id}/members`)
    return data
  },

  async addMember(id: number, userId: number): Promise<WorkspaceMember> {
    const { data } = await apiClient.post<WorkspaceMember>(`/admin/workspaces/${id}/members`, {
      user_id: userId
    })
    return data
  },

  async removeMember(id: number, userId: number): Promise<void> {
    await apiClient.delete(`/admin/workspaces/${id}/members/${userId}`)
  },

  async listGrants(id: number): Promise<WorkspaceGrant[]> {
    const { data } = await apiClient.get<WorkspaceGrant[]>(`/admin/workspaces/${id}/grants`)
    return data
  },

  async upsertGrant(id: number, payload: UpsertGrantRequest): Promise<WorkspaceGrant> {
    const { data } = await apiClient.put<WorkspaceGrant>(`/admin/workspaces/${id}/grants`, payload)
    return data
  },

  async removeGrant(id: number, groupId: number): Promise<void> {
    await apiClient.delete(`/admin/workspaces/${id}/grants/${groupId}`)
  },

  /**
   * vendor 自读本工作区。
   *
   * 返回里的 settlement_rate_min/max 是自设账号倍率的可调区间；调价本身
   * 走账号更新接口（accountsAPI.update 的 rate_multiplier），工作区侧
   * 没有调价端点 —— 那会让供应商绕过账号维度自定结算区间。
   */
  async getMine(): Promise<MyWorkspace> {
    const { data } = await apiClient.get<MyWorkspace>('/admin/workspaces/me')
    return data
  }
}

export default adminWorkspacesAPI
