<template>
  <SurfacePlaceholder
    title="Access Admin"
    tone="must-build"
    summary="This is intentionally a separate route family, not more tabs inside the operator dashboard. It owns instance-level multi-user administration for one Engram install."
    next-step="Use `.od/access-admin.html` as the visual contract and map the first live-backed subset only when the instance-level multi-user backend seams are ready."
    :seams="[
      'future users/invitations/providers/sessions/audit routes',
      'current auth bootstrap and admin endpoint truth',
      'module boundary: still Engram-instance scope, not platform scope'
    ]"
  />
</template>
