// The part of markdown-it's API the page uses.
type Token = { type: string; tag: string; content: string; info: string; markup: string; block: boolean; attrGet(n: string): string | null; attrSet(n: string, v: string): void; attrs: [string, string][] | null }
export type Rule = (tokens: Token[], idx: number, options: unknown, env: unknown, self: { renderToken(t: Token[], i: number, o: unknown): string }) => string
type MD = {
  parse(src: string, env: object): Token[]
  renderer: { rules: Record<string, Rule | undefined>; render(tokens: Token[], options: unknown, env: object): string }
  inline: { ruler: { after(name: string, rule: string, fn: (state: any, silent: boolean) => boolean): void } }
  block: { ruler: { after(name: string, rule: string, fn: (state: any, start: number, end: number, silent: boolean) => boolean, o?: object): void } }
  utils: { escapeHtml(s: string): string }
  options: unknown
}
declare const markdownit: (o?: object) => MD
export default markdownit
