---
name: gsap-animation
description: Use when implementing or debugging GSAP animations, timelines, scroll-driven motion, or GSAP integration with React and Next.js.
---

# GSAP Animation

Use GSAP only when it fits the requested interaction and the project's stack.
Inspect the installed GSAP version, framework, and existing animation setup
before choosing APIs or adding dependencies. Consult the official GSAP docs for
version-sensitive details and plugins.

## Core practices

- Prefer transforms and opacity for frequent animation; avoid animating layout
  properties when a transform can achieve the same effect.
- Use timelines to express related sequences and make start positions explicit.
- Scope selectors to the component or element being animated. In React, use
  `useGSAP` or a scoped GSAP context and clean up on unmount so animations do
  not leak across renders.
- For scroll-linked behavior, use ScrollTrigger and account for refresh,
  pinning, responsive layouts, and reduced-motion preferences.
- Respect `prefers-reduced-motion`; provide a useful static state when motion
  is reduced or unavailable.
- Do not register or use a premium plugin unless it is installed and licensed
  in the project. Do not assume a plugin is available because it appears in an
  example.
- Keep animation nonessential to comprehension and interaction. Preserve
  keyboard access and avoid motion that blocks the user from acting.

## References

- Official GSAP skills: https://github.com/greensock/gsap-skills
- GSAP documentation: https://gsap.com/docs/v3/
- React integration: https://gsap.com/resources/React/
- ScrollTrigger: https://gsap.com/docs/v3/Plugins/ScrollTrigger/
