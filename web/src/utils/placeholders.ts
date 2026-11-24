// 含 URL 的输入框文案，经脚本常量绑定到模板。
// 不要在模板的字符串属性里直接写 "//"（如 placeholder="https://..."）：vue-tsc 3.3 生成组件 props 的
// `// @ts-ignore` 副本时会把字符串里的 "//" 改写，打乱后续代码的类型流，
// 之后模板里经 `.` / `[]` 读取的普通常量会被误判为 ref。
export const URL_TEXT = {
  site: 'https://example.com',
  proxy: 'socks5://127.0.0.1:1080 或 http://user:pass@host:port',
  telegramApi: 'https://api.telegram.org',
  barkDevice: 'https://bark.example.com/xxxxxxxx',
  barkHelp: 'Bark App 里复制的 https://<服务器>/<设备 Key>，多台设备用逗号或换行分隔',
  barkIcon: 'https://example.com/icon.png',
  flaresolverr: 'http://127.0.0.1:8191',
  externalSolver: 'https://solver.example.com/solve',
} as const
