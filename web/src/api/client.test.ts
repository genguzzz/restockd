// client.ts 请求封装单元测试：mock 全局 fetch
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, buildUrl, request, setUnauthorizedHandler } from './client'

type FetchMock = ReturnType<typeof vi.fn>

function respond(status: number, body: string): Response {
  return new Response(body, { status, headers: { 'Content-Type': 'application/json' } })
}

describe('buildUrl', () => {
  it('returns path unchanged without query', () => {
    expect(buildUrl('/api/sites')).toBe('/api/sites')
    expect(buildUrl('/api/sites', {})).toBe('/api/sites')
  })
  it('skips empty, null and undefined values', () => {
    expect(buildUrl('/api/products', { site_id: 'a b', status: '', q: undefined, x: null, limit: 0 })).toBe(
      '/api/products?site_id=a+b&limit=0',
    )
  })
  it('returns bare path when every value is empty', () => {
    expect(buildUrl('/api/watches', { site_id: undefined })).toBe('/api/watches')
  })
})

describe('request', () => {
  let fetchMock: FetchMock
  beforeEach(() => {
    fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    setUnauthorizedHandler(null)
  })

  it('parses JSON on success', async () => {
    fetchMock.mockResolvedValue(respond(200, '{"sites":2}'))
    await expect(request('/api/stats', { query: { a: 'x' } })).resolves.toEqual({ sites: 2 })
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/stats?a=x')
    expect(init.method).toBe('GET')
    expect(init.credentials).toBe('include')
    expect(init.body).toBeUndefined()
  })

  it('returns null for empty body', async () => {
    fetchMock.mockResolvedValue(new Response('', { status: 200 }))
    await expect(request('/api/x')).resolves.toBeNull()
  })

  it('throws ApiError with server message on 400', async () => {
    fetchMock.mockResolvedValue(respond(400, '{"error":"站点不存在"}'))
    const error = await request('/api/sites/x').catch((err: unknown) => err)
    expect(error).toBeInstanceOf(ApiError)
    expect((error as ApiError).status).toBe(400)
    expect((error as ApiError).message).toBe('站点不存在')
  })

  it('falls back to HTTP status text for non-JSON errors', async () => {
    fetchMock.mockResolvedValue(new Response('<html>oops</html>', { status: 500 }))
    const error = await request('/api/sites').catch((err: unknown) => err)
    expect(error).toBeInstanceOf(ApiError)
    expect((error as ApiError).status).toBe(500)
    expect((error as ApiError).message).toBe('HTTP 500')
  })

  it('invokes the unauthorized handler on 401', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    fetchMock.mockResolvedValue(respond(401, '{"error":"未登录"}'))
    const error = await request('/api/me').catch((err: unknown) => err)
    expect(handler).toHaveBeenCalledTimes(1)
    expect((error as ApiError).status).toBe(401)
    expect((error as ApiError).message).toBe('未登录')
  })

  it('does not invoke the unauthorized handler on other errors', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    fetchMock.mockResolvedValue(respond(403, '{"error":"forbidden"}'))
    await request('/api/me').catch(() => undefined)
    expect(handler).not.toHaveBeenCalled()
  })

  it('sends JSON body with Content-Type on POST', async () => {
    fetchMock.mockResolvedValue(respond(200, '{"ok":true}'))
    await request('/api/login', { method: 'POST', body: { password: 'p' } })
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/login')
    expect(init.method).toBe('POST')
    expect(init.body).toBe('{"password":"p"}')
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json')
    expect((init.headers as Record<string, string>).Accept).toBe('application/json')
  })
})
