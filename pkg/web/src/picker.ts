// The model picker of the design system: the connected models in provider
// sections with the default effort of each, and the effort of the one picked.

import { type Conf, type Model, h, icon } from "./api"

type Params = Record<string, unknown>
declare const MPPicker: { init: (p: HTMLElement, t: HTMLElement, sub: HTMLElement) => void }

const obj = (v: unknown): v is Params => typeof v === "object" && v !== null && !Array.isArray(v)
// holds and merge are the TUI's: params has every value of sub; patch is
// set over params, objects key by key.
const holds = (p: Params, sub: Params): boolean => Object.entries(sub).every(([k, v]) => (obj(p[k]) && obj(v) ? holds(p[k] as Params, v) : JSON.stringify(p[k]) === JSON.stringify(v)))
const merge = (p: Params, patch: Params): Params => ({ ...p, ...Object.fromEntries(Object.entries(patch).map(([k, v]) => [k, obj(p[k]) && obj(v) ? merge(p[k] as Params, v) : v])) })

const efforts = (m?: Model) => m?.settings?.find(s => s.name === "effort")?.values ?? []
const said = (name: string) => (name === "xhigh" ? "Extra high" : name ? name[0].toUpperCase() + name.slice(1) : "")
const effort = (m: Model | undefined, p: Params) => said(efforts(m).find(v => holds(p, v.params))?.name ?? "")

let made = 0

// picker is the trigger for a form row, the popovers to put in the
// positioned box over the view (the sheet's scrim), and the config picked.
// A model the catalog does not list stays as it is until another is picked.
export function picker(models: Model[], cur: Conf | undefined, last: string | null): { trigger: HTMLElement; pops: HTMLElement[]; value: () => Conf | undefined } {
  const id = `pk${++made}`
  const has = cur && (cur.preset ?? cur.model)
  let chosen = models.find(m => m.id === (cur ? has : last)) ?? (cur ? undefined : models[0])
  let params = ((cur && chosen?.id === has ? cur.params : chosen?.config.params) ?? {}) as Params

  const name = chosen?.name ?? cur?.model ?? "No model connected"
  const eff = h("span", { className: "pk-effort" }, effort(chosen, params))
  const trigger = h("button", { type: "button", className: "select plain model-trigger", disabled: !models.length }, name, eff)
  trigger.setAttribute("aria-haspopup", "dialog")
  trigger.setAttribute("aria-expanded", "false")
  trigger.dataset.picker = id

  const byRow = new Map<Element, Model>()
  const groups = [...new Set(models.map(m => m.provider))].map((section, g) => {
    const label = h("div", { className: "pk-label", id: `${id}-g${g}`, role: "presentation" }, section)
    const group = h("div", { role: "group" }, label, ...models.filter(m => m.provider === section).map((m, i) => {
      const r = h("div", { role: "option", id: `${id}-g${g}-${i}`, className: "pk-row", ariaSelected: String(m === chosen) }, h("span", { className: "pk-name" }, m.name), h("span", { className: "pk-hint" }, effort(m, (m.config.params ?? {}) as Params)))
      byRow.set(r, m)
      return r
    }))
    group.setAttribute("aria-labelledby", label.id)
    return group
  })
  const input = h("input", { role: "combobox", placeholder: "Search models", ariaLabel: "Search models", spellcheck: false, autocomplete: "off" })
  input.setAttribute("aria-controls", `${id}-list`)
  const more = h("div", { role: "option", className: "pk-row pk-more" }, h("span", { className: "pk-name" }, "Effort"), h("span", { className: "pk-hint" }, eff.textContent!), icon("chevron-right"))
  more.lastElementChild!.classList.add("pk-chev")
  more.setAttribute("aria-haspopup", "listbox")
  more.setAttribute("aria-controls", `${id}-effort`)
  more.setAttribute("aria-expanded", "false")
  const sub = h("div", { className: "model-picker pk-sub pk-anchored", id: `${id}-effort`, role: "listbox", ariaLabel: "Effort", hidden: true })
  // drawEfforts lists the values of the chosen model; it has the row only if it has them.
  const drawEfforts = () => {
    const now = effort(chosen, params)
    sub.replaceChildren(...efforts(chosen).map(v => h("div", { role: "option", className: "pk-row", ariaSelected: String(said(v.name) === now) }, h("span", { className: "pk-name" }, said(v.name)))))
    more.hidden = !sub.childElementCount
    more.querySelector(".pk-hint")!.textContent = eff.textContent = now
  }
  drawEfforts()
  const pop = models.length
    ? h("div", { className: "model-picker pk-anchored", id, role: "dialog", ariaLabel: "Model", hidden: true },
        h("label", { className: "pk-search" }, icon("search"), input),
        h("div", { className: "pk-list", role: "listbox", id: `${id}-list`, ariaLabel: "Models" }, ...groups),
        h("div", { className: "pk-foot" }, more))
    : h("div", { hidden: true })
  pop.addEventListener("mp-pick", e => {
    const r = (e as CustomEvent<Element>).detail
    const m = byRow.get(r)
    if (m) {
      chosen = m
      params = (m.config.params ?? {}) as Params
    } else {
      params = merge(params, efforts(chosen).find(v => said(v.name) === r.textContent)!.params as Params)
    }
    drawEfforts()
  })
  if (models.length) MPPicker.init(pop, trigger, sub)
  return {
    trigger,
    pops: [pop, sub],
    value: () => chosen && { ...(chosen.id === has ? cur! : chosen.config), params },
  }
}
