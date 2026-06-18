---
title: Weekly Active Users (WAU)
type: Metric
description: Count of distinct customers who placed at least one order in a rolling 7-day window.
resource: https://lookerstudio.google.com/reporting/acme-wau
tags: [growth, engagement, kpi]
timestamp: 2026-06-01T00:00:00Z
---

# Weekly Active Users (WAU)

**Definition:** distinct `customer_id` values in [[tables/orders]] where `created_at` falls in the last 7 calendar days and `status` != `cancelled`.

## SQL

```sql
SELECT
  DATE_TRUNC(created_at, WEEK) AS week,
  COUNT(DISTINCT customer_id) AS wau
FROM `acme-prod.sales.orders`
WHERE status != 'cancelled'
GROUP BY 1
ORDER BY 1 DESC
```

## Caveats

- Counts an order, not a session — a customer with 3 orders in a week counts once.
- Excludes test accounts (emails ending in `@acme-internal.com`).

## Source tables

- [[tables/orders]] — event source
- [[tables/customers]] — used for test-account exclusion
