---
title: Customers Table
type: BigQuery Table
description: Customer master data. One row per registered account; deduped on email.
resource: https://console.cloud.google.com/bigquery?project=acme-prod&d=sales&t=customers
tags: [sales, customers, identity]
timestamp: 2026-06-01T00:00:00Z
---

# Customers Table

Part of [[datasets/sales]].

## Schema

| Column | Type | Description |
|--------|------|-------------|
| `customer_id` | STRING | Globally unique identifier (UUID). |
| `email` | STRING | Deduplicated on registration; case-normalised to lowercase. |
| `created_at` | TIMESTAMP | UTC timestamp of first registration. |
| `country` | STRING | ISO 3166-1 alpha-2 country code. |

## Referenced by

- [[tables/orders]] — joins on `customer_id`
- [[metrics/weekly-active-users]] — active customer filter
