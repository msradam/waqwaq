---
title: Sales Dataset
type: Dataset
description: Customer orders, revenue, and fulfilment data for Acme's commerce platform.
resource: https://console.cloud.google.com/bigquery?project=acme-prod&d=sales
tags: [sales, revenue, commerce]
timestamp: 2026-06-01T00:00:00Z
---

# Sales Dataset

Acme's primary commerce dataset. One schema per business domain; tables join on `customer_id`.

## Tables

- [[tables/orders]] — one row per completed order
- [[tables/customers]] — customer master data

## Notes

All timestamps are UTC. Revenue fields are in USD cents to avoid floating-point rounding.
