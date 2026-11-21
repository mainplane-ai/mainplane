// An .svg the page imports is its text (esbuild --loader:.svg=text).
declare module "*.svg" {
  const text: string
  export default text
}
