import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { DomainMonitorDashboard } from '@/components/DomainMonitorDashboard'

import './index.css'

// If the dashboard is served under a base path (e.g. "/ddup/"), the API is exposed under the same path.
// This derives the base path from the URL of the current page by stripping the last segment.
const endpoint = window.location.pathname.replace(/\/[^/]*$/, '')

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <DomainMonitorDashboard endpoint={endpoint} />
  </StrictMode>
)
