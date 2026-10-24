---
name: frontend-design
description: Use when building or changing a frontend interface, landing page, dashboard, component, or responsive layout, especially when the user provides a visual reference or asks for stronger visual design.
---

# Frontend Design

Build interfaces that feel specific to the product and work in the real
application. Apply this skill to web frontends; follow the repository's stack,
design system, and existing conventions instead of introducing a new stack.

## Workflow

1. Inspect the app entry points, package scripts, installed UI libraries,
   existing styles, and relevant project instructions before editing.
2. Identify the audience, task, content hierarchy, and requested visual
   direction. If details are missing, make a coherent choice that fits the
   product and state it briefly.
3. Choose a clear art direction before implementation: layout, typography,
   color palette, spacing rhythm, imagery, and interaction behavior. Use a
   small set of intentional design decisions rather than defaulting to a
   generic centered hero, gradient, or repeated card grid.
4. Implement the actual states and content, including loading, empty, error,
   and success states when the interface needs them. Reuse existing tokens and
   components; keep responsive behavior and accessibility in the design.
5. Run the project's relevant tests, lint, typecheck, or build checks. When a
   browser or screenshot tool is available, open the app at representative
   desktop and mobile sizes, inspect the rendered result, and fix visible
   issues before finishing. Never claim a visual check that was not performed.

## Implementation guidance

- Prefer semantic HTML, keyboard-operable controls, visible focus, useful
  labels, sufficient contrast, and reduced-motion support.
- Use the framework and styling approach already present. Do not add a UI
  dependency just to avoid using the project's existing components.
- When the project uses shadcn/ui, inspect its existing `components.json`,
  aliases, theme variables, and installed components before adding anything.
  Use the official installation or component instructions that match the
  detected framework; do not scaffold over an existing app.
- Use real project assets when available. Do not invent product claims, logos,
  or imagery and present them as supplied assets.
- Keep animation purposeful and restrained. Use CSS for simple transitions;
  use GSAP when sequencing or scroll-linked behavior calls for it, and follow
  the `gsap-animation` skill when available.
- Before completion, summarize the files changed and checks performed, and
  disclose any visual review that could not be run.

## References

- shadcn/ui installation: https://ui.shadcn.com/docs/installation
- Vercel Agent Skills: https://github.com/vercel-labs/skills
- Frontend design reference: https://github.com/anthropics/claude-code/tree/main/plugins/frontend-design/skills/frontend-design
- Visual design workflow reference: https://github.com/leonxlnx/taste-skill
