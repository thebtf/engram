<template>
  <SurfacePlaceholder
    title="Platform Hub"
    tone="must-build"
    summary="This surface family is intentionally separated from the operator module because it is meant to grow into a cross-product SaaS administration hub, not remain forever inside Engram."
    next-step="Keep the route family and module seam visible now; later this becomes a separate module and then a separate project with its own backend contracts."
    :seams="[
      '.od/saas-admin.html as visual contract',
      'future workspaces/plans/billing/payments/revenue/audit bounded context',
      'shared admin-shell primitives, but separate product authority'
    ]"
  />
</template>
