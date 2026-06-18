---
title: Marketing Dataset
type: Dataset
description: Campaign performance, attribution windows, and channel spend for Acme's marketing org.
resource: https://console.cloud.google.com/bigquery?project=acme-prod&d=marketing
tags: [marketing, attribution, campaigns]
timestamp: 2026-06-01T00:00:00Z
---

# Marketing Dataset

Attribution and campaign data. Joins to [[datasets/sales]] on `customer_id` via a 30-day lookback window.

## Caveats

Last-touch attribution only. Multi-touch is modelled separately in the [[metrics/weekly-active-users]] metric.
