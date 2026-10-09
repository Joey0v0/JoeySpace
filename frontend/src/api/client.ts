import { session } from '../auth/session.ts'
import type { createSession } from '../auth/session.ts'

export interface Profile { id: string; username: string; nickname: string }
export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) { super(message); this.name = 'ApiError'; this.status = status }
}
export class StaleRequestError extends Error { constructor() { super('请求身份已经改变'); this.name = 'StaleRequestError' } }
export function isId(value: unknown): value is string { return typeof value === 'string' && /^[1-9]\d*$/.test(value) }
export function errorText(error: unknown): string {
  if (error instanceof ApiError) return error.message
  return '暂时无法完成请求，请重试'
}
function statusMessage(status: number) {
  if (status === 401) return '登录已失效，或用户名、密码不正确'
  if (status === 403) return '当前账号无权访问此资源'
  if (status === 404) return '会话不存在或当前账号无法访问'
  if (status === 503) return '服务暂时不可用，请稍后重试'
  if (status === 504) return '服务响应超时，请重试'
  return '请求失败，请重试'
}
export function createApiClient(identity: ReturnType<typeof createSession>, transport: typeof fetch = globalThis.fetch) {
  async function request<T>(path: string, options: RequestInit = {}, authenticated = true): Promise<T> {
    const epoch = identity.version()
    const token = identity.token()
    const controller = new AbortController()
    const timeout = setTimeout(() => controller.abort(), 15000)
    const headers = new Headers(options.headers)
    if (authenticated && token) headers.set('Authorization', 'Bearer ' + token)
    if (options.body) headers.set('Content-Type', 'application/json')
    const unchanged = () => { if (epoch !== identity.version()) throw new StaleRequestError() }
    try {
      const response = await transport('/api/v1' + path, { ...options, headers, signal: controller.signal })
      unchanged()
      if (!response.ok) {
        if (response.status === 401 && authenticated) identity.clearSession()
        throw new ApiError(response.status, statusMessage(response.status))
      }
      const envelope = await response.json()
      unchanged()
      if (envelope?.code !== 0 || !('data' in envelope)) throw new ApiError(502, '服务返回的数据无效，请重试')
      return envelope.data as T
    } catch (error) {
      if (error instanceof ApiError || error instanceof StaleRequestError) throw error
      unchanged()
      throw new ApiError(0, controller.signal.aborted ? '请求超时，请重试' : '网络连接失败，请重试')
    } finally { clearTimeout(timeout) }
  }
  async function getMyInfo(): Promise<Profile> {
    const profile = await request<Profile>('/user/info')
    if (!profile || !isId(profile.id) || typeof profile.username !== 'string' || typeof profile.nickname !== 'string') throw new ApiError(502, '本人资料无效，请重试')
    return profile
  }
  async function login(username: string, password: string): Promise<Profile> {
    identity.clearSession()
    const data = await request<{ token: string }>('/user/login', { method: 'POST', body: JSON.stringify({ username, password }) }, false)
    if (!data || typeof data.token !== 'string' || !data.token) throw new ApiError(502, '登录结果无效，请重试')
    identity.setSession(data.token)
    const epoch = identity.version()
    try { return await getMyInfo() }
    catch (error) { if (identity.version() === epoch) identity.clearSession(); throw error }
  }
  return { request, getMyInfo, login }
}
export const api = createApiClient(session)
let verifiedEpoch = -1
let pending: { epoch: number; promise: Promise<Profile> } | undefined
export let currentProfile: Profile | null = null
session.subscribe(() => { currentProfile = null; verifiedEpoch = -1; pending = undefined })
export async function verifySession(): Promise<Profile> {
  if (verifiedEpoch === session.version() && currentProfile) return currentProfile
  const epoch = session.version()
  if (pending?.epoch === epoch) return pending.promise
  const promise = api.getMyInfo().then(profile => {
    if (session.version() !== epoch) throw new StaleRequestError()
    currentProfile = profile
    verifiedEpoch = epoch
    return profile
  }).finally(() => { if (pending?.epoch === epoch) pending = undefined })
  pending = { epoch, promise }
  return promise
}
