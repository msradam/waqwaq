---
title: Orders Table
type: BigQuery Table
description: One row per completed customer order. Source of truth for revenue reporting.
resource: https://console.cloud.google.com/bigquery?project=acme-prod&d=sales&t=orders
tags: [sales, orders, revenue]
timestamp: 2026-06-01T00:00:00Z
---

# Orders Table

Part of [[datasets/sales]].

## Schema

| Column | Type | Description |
|--------|------|-------------|
| `order_id` | STRING | Globally unique order identifier (UUID). |
| `customer_id` | STRING | FK → [[tables/customers]]. |
| `created_at` | TIMESTAMP | UTC timestamp when the order was placed. |
| `total_cents` | INT64 | Order total in USD cents (avoids float rounding). |
| `status` | STRING | `pending`, `shipped`, `delivered`, `cancelled`. |

## Join paths

```sql
orders o
  JOIN customers c ON o.customer_id = c.customer_id
```
