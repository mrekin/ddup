import { useState, useEffect, useCallback } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from '@/ui/card'
import { Badge } from '@/ui/badge'
import { Button } from '@/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/ui/dropdown-menu'
import {
  RefreshCw,
  Activity,
  AlertTriangle,
  BellOff,
  BellRing,
  CheckCircle,
  XCircle,
  Clock,
  Search,
  Play,
  ChevronDown,
  type LucideIcon,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import { setFavicon } from '@/lib/favicon'
import type { OverallStatus } from '@/lib/favicon-shapes'
import { StatusIcon } from '@/components/StatusIcon'

interface DomainStatusEndpoint {
  healthy: boolean
  ip: string
  failureCount?: number
  // True if this endpoint's value is currently written in the DNS record (CNAME records hold a single value)
  inDns?: boolean
}

interface DomainStatus {
  lastUpdated: string
  provider: string
  error?: string
  endpoints: DomainStatusEndpoint[]
}

type DomainsResponse = Record<string, DomainStatus>

type Domain = {
  name: string
  status: DomainStatus
}

// Delivery status of push notifications, from GET /api/notifications
// The backend never exposes the destination server or topic here
interface NotificationsStatus {
  enabled: boolean
  ok?: boolean
  lastAttempt?: string
  lastSuccess?: string
  lastError?: string
}

// One segment of the summary bar: the count of domains with a health status
// The label is only shown from the sm breakpoint up, and stays available to screen readers
const SummarySegment = ({
  icon: Icon,
  label,
  title,
  count,
  className,
}: {
  icon: LucideIcon
  label: string
  title: string
  count: number
  className: string
}) => (
  <div
    title={title}
    className={cn(
      'flex items-center justify-center gap-1.5 px-1 py-1.5 sm:gap-2.5 sm:p-2.5 lg:gap-3 lg:p-3.5',
      className
    )}
  >
    <Icon className="h-4 w-4 shrink-0 sm:h-5 sm:w-5 lg:h-6 lg:w-6" aria-hidden="true" />
    <span className="text-base leading-none font-bold sm:text-2xl lg:text-3xl">{count}</span>
    <span className="sr-only text-[13px] font-medium text-muted-foreground sm:not-sr-only lg:text-sm">{label}</span>
  </div>
)

const DomainMonitorDashboard = ({ endpoint }: { endpoint: string }) => {
  const [domains, setDomains] = useState<Domain[]>([])
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [searchTerm, setSearchTerm] = useState('')
  const [isChecking, setIsChecking] = useState(false)
  const [notifications, setNotifications] = useState<NotificationsStatus | null>(null)
  const [isSendingNotification, setIsSendingNotification] = useState(false)
  const [notificationSent, setNotificationSent] = useState(false)

  const fetchDomains = useCallback(async (): Promise<void> => {
    setIsLoading(true)
    setError(null) // Clear previous errors
    try {
      const response = await fetch(endpoint + '/api/status')
      if (!response.ok) {
        throw new Error(`HTTP error: ${response.status} ${response.statusText}`)
      }
      const data: DomainsResponse = await response.json()

      // Convert the response format to our Domain array
      const domainsArray: Domain[] = Object.entries(data).map(([name, status]) => ({
        name,
        status,
      }))

      setDomains(domainsArray)
      setLastUpdated(new Date())
    } catch (error) {
      console.error('Failed to fetch domains data:', error)
      const errorMessage = error instanceof Error ? error.message : 'Unknown error occurred'
      setError(`Failed to fetch domain data: ${errorMessage}`)

      // Fallback to empty array on error
      setDomains([])
    } finally {
      setIsLoading(false)
    }
  }, [endpoint])

  // Fetches the notification delivery status; failures are tolerated, since the indicator is optional
  const fetchNotifications = useCallback(async (signal?: AbortSignal): Promise<void> => {
    try {
      const response = await fetch(endpoint + '/api/notifications', { signal })
      if (!response.ok) {
        // Older backends don't have this endpoint: hide the indicator
        setNotifications(null)
        return
      }
      const data: NotificationsStatus = await response.json()
      setNotifications(data.enabled ? data : null)
    } catch {
      // Keep showing the last known status on network errors
    }
  }, [endpoint])

  useEffect(() => {
    if (!autoRefresh) {
      return
    }

    const controller = new AbortController()

    const runFetch = async () => {
      try {
        setIsLoading(true)
        setError(null)

        const response = await fetch(endpoint + '/api/status', { signal: controller.signal })
        if (!response.ok) {
          throw new Error(`HTTP error: ${response.status} ${response.statusText}`)
        }

        const data: DomainsResponse = await response.json()
        if (controller.signal.aborted) {
          return
        }

        const domainsArray: Domain[] = Object.entries(data).map(([name, status]) => ({ name, status }))
        setDomains(domainsArray)
        setLastUpdated(new Date())
      } catch (err) {
        if (controller.signal.aborted || (err instanceof DOMException && err.name === 'AbortError')) {
          return
        }

        console.error('Failed to fetch domains data:', err)

        const errorMessage = err instanceof Error ? err.message : 'Unknown error occurred'
        setError(`Failed to fetch domain data: ${errorMessage}`)
        setDomains([])
      } finally {
        if (!controller.signal.aborted) {
          setIsLoading(false)
        }
      }
    }

    // Defer the initial fetch so the effect body itself does not call setState synchronously.
    queueMicrotask(() => {
      void runFetch()
      void fetchNotifications(controller.signal)
    })

    const interval = setInterval(() => {
      if (autoRefresh) {
        void runFetch()
        void fetchNotifications(controller.signal)
      }
    }, 60000)

    return () => {
      controller.abort()
      clearInterval(interval)
    }
  }, [autoRefresh, endpoint, fetchNotifications])

  const refreshClicked = async () => {
    await fetchDomains()
    void fetchNotifications()
  }

  // Asks the server to run health checks right now (instead of waiting for the next interval), then shows the result
  const checkNowClicked = async (): Promise<void> => {
    setIsChecking(true)
    setError(null)
    try {
      const response = await fetch(endpoint + '/api/check', {
        method: 'POST',
        // The server requires this header, which blocks cross-site requests
        headers: { 'X-Requested-By': 'ddup-dashboard' },
      })
      if (!response.ok) {
        throw new Error(`HTTP error: ${response.status} ${response.statusText}`)
      }

      // The response includes the updated status
      const data: DomainsResponse = await response.json()
      setDomains(Object.entries(data).map(([name, status]) => ({ name, status })))
      setLastUpdated(new Date())
      // A check may have triggered DNS updates and notification attempts
      void fetchNotifications()
    } catch (err) {
      console.error('Failed to run check:', err)
      const errorMessage = err instanceof Error ? err.message : 'Unknown error occurred'
      setError(`Failed to run check: ${errorMessage}`)
    } finally {
      setIsChecking(false)
    }
  }

  // Asks the server to send a notification with the current status of all domains
  const testNotificationClicked = async (): Promise<void> => {
    setIsSendingNotification(true)
    setError(null)
    setNotificationSent(false)
    try {
      const response = await fetch(endpoint + '/api/notifications/test', {
        method: 'POST',
        // The server requires this header, which blocks cross-site requests
        headers: { 'X-Requested-By': 'ddup-dashboard' },
      })
      if (!response.ok) {
        throw new Error(`HTTP error: ${response.status} ${response.statusText}`)
      }

      // The delivery is asynchronous: the status chip updates on the next poll
      setNotificationSent(true)
      void fetchNotifications()
      setTimeout(() => setNotificationSent(false), 4000)
    } catch (err) {
      console.error('Failed to send test notification:', err)
      const errorMessage = err instanceof Error ? err.message : 'Unknown error occurred'
      setError(`Failed to send test notification: ${errorMessage}`)
    } finally {
      setIsSendingNotification(false)
    }
  }

  const getDomainStatus = (domain: Domain) => {
    if (domain.status.error) {
      return 'unhealthy'
    }
    if (domain.status.endpoints.length === 0) {
      return 'unhealthy'
    }

    const healthyEndpoints = domain.status.endpoints.filter((e) => e.healthy).length
    const totalEndpoints = domain.status.endpoints.length

    if (healthyEndpoints === totalEndpoints) {
      return 'healthy'
    }
    if (healthyEndpoints > 0) {
      return 'warning'
    }
    return 'unhealthy'
  }

  const filteredDomains = domains.filter((domain) => {
    const searchLower = searchTerm.toLowerCase()
    const domainMatches = domain.name.toLowerCase().includes(searchLower)
    const endpointMatches = domain.status.endpoints.some((endpoint) => endpoint.ip.toLowerCase().includes(searchLower))
    return domainMatches || endpointMatches
  })

  // The icon of the tab shows the worst status of all the domains (not only the ones matching the search)
  const overallCounts = {
    warning: domains.filter((d) => getDomainStatus(d) === 'warning').length,
    unhealthy: domains.filter((d) => getDomainStatus(d) === 'unhealthy').length,
  }
  let overallStatus: OverallStatus = 'unknown'
  if (error || overallCounts.unhealthy > 0) {
    overallStatus = 'unhealthy'
  } else if (overallCounts.warning > 0) {
    overallStatus = 'warning'
  } else if (domains.length > 0) {
    overallStatus = 'healthy'
  }
  useEffect(() => {
    setFavicon(overallStatus)
  }, [overallStatus])

  const healthyDomains = filteredDomains.filter((d) => getDomainStatus(d) === 'healthy').length
  const warningDomains = filteredDomains.filter((d) => getDomainStatus(d) === 'warning').length
  const unhealthyDomains = filteredDomains.filter((d) => getDomainStatus(d) === 'unhealthy').length

  return (
    <div className="min-h-screen bg-background p-4 md:p-6">
      <div className="mx-auto max-w-7xl space-y-6">
        {/* Header */}
        <div className="flex flex-col gap-4 md:flex-row md:items-center md:justify-between">
          <div className="flex items-center gap-3">
            <StatusIcon status={overallStatus} className="h-8 w-8 shrink-0" />
            <h1 className="text-3xl font-bold tracking-tight">ddup</h1>
          </div>

          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            {lastUpdated && (
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                <Clock className="h-4 w-4" />
                Last updated: {lastUpdated.toLocaleTimeString()}
              </div>
            )}

            {/* Delivery status of push notifications; hidden until the backend reports at least one attempt */}
            {notifications && typeof notifications.ok === 'boolean' && (
              <div
                className={cn(
                  'flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs font-medium',
                  notifications.ok
                    ? 'border-green-200 bg-green-50 text-green-800 dark:border-green-800 dark:bg-green-950/50 dark:text-green-300'
                    : 'border-red-200 bg-red-50 text-red-800 dark:border-red-800 dark:bg-red-950/50 dark:text-red-300'
                )}
                title={
                  notifications.ok
                    ? `Notifications are being delivered${notifications.lastSuccess ? `: last sent ${new Date(notifications.lastSuccess).toLocaleTimeString()}` : ''}`
                    : `Notifications are not being delivered${notifications.lastError ? `: ${notifications.lastError}` : ''}${notifications.lastAttempt ? ` (at ${new Date(notifications.lastAttempt).toLocaleTimeString()})` : ''}`
                }
              >
                {notifications.ok ? <BellRing className="h-3.5 w-3.5" /> : <BellOff className="h-3.5 w-3.5" />}
                {notifications.ok ? 'Notifications' : 'Notifications unavailable'}
              </div>
            )}

            <div className="flex gap-2">
              {/* Split button: the main part toggles auto-refresh, the chevron opens a menu to refresh right away */}
              <div className="flex">
                <Button
                  variant={autoRefresh ? 'default' : 'outline'}
                  size="sm"
                  onClick={() => setAutoRefresh(!autoRefresh)}
                  className="flex items-center gap-2 rounded-r-none"
                >
                  <Activity className="h-4 w-4" />
                  Auto-refresh {autoRefresh ? 'ON' : 'OFF'}
                </Button>

                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button
                      variant={autoRefresh ? 'default' : 'outline'}
                      size="sm"
                      aria-label="More refresh options"
                      className={cn(
                        'rounded-l-none',
                        autoRefresh ? 'border-l border-primary-foreground/20' : 'border-l-0'
                      )}
                    >
                      <ChevronDown className="h-4 w-4" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem onSelect={refreshClicked} disabled={isLoading}>
                      <RefreshCw className="h-4 w-4" />
                      Refresh now
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>

              <Button
                variant="outline"
                size="sm"
                onClick={checkNowClicked}
                disabled={isChecking || isLoading}
                title="Run health checks now, instead of waiting for the next interval"
                className="flex items-center gap-2"
              >
                <Play className={cn('h-4 w-4', isChecking && 'animate-pulse')} />
                {isChecking ? 'Checking…' : 'Check now'}
              </Button>

              {/* Only shown when notifications are configured (the backend reports them as enabled) */}
              {notifications && (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={testNotificationClicked}
                  disabled={isSendingNotification || isLoading}
                  title="Send a notification with the current status of all domains"
                  className="flex items-center gap-2"
                >
                  <BellRing className={cn('h-4 w-4', isSendingNotification && 'animate-pulse')} />
                  {isSendingNotification ? 'Sending…' : notificationSent ? 'Sent ✓' : 'Test notification'}
                </Button>
              )}
            </div>
          </div>
        </div>

        {/* Search bar */}
        <div className="relative w-full max-w-md">
          <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <input
            type="text"
            placeholder="Search domains or IP addresses..."
            value={searchTerm}
            onChange={(e) => setSearchTerm(e.target.value)}
            className="w-full rounded-md border border-input bg-background px-10 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          />
        </div>

        {/* Summary of the domains: three equal segments in one bar */}
        {!error && domains.length > 0 && (
          <div className="grid grid-cols-3 divide-x overflow-hidden rounded-md border bg-card">
            <SummarySegment
              icon={CheckCircle}
              label="Healthy"
              title="Healthy domains"
              count={healthyDomains}
              className="text-green-600 dark:text-green-400"
            />
            <SummarySegment
              icon={AlertTriangle}
              label="Warning"
              title="Domains with some unhealthy endpoints"
              count={warningDomains}
              className="text-yellow-600 dark:text-yellow-400"
            />
            <SummarySegment
              icon={XCircle}
              label="Unhealthy"
              title="Unhealthy domains"
              count={unhealthyDomains}
              className="text-red-600 dark:text-red-400"
            />
          </div>
        )}

        {/* Error Message */}
        {error && (
          <Card className="border-red-200 bg-red-50 dark:border-red-800 dark:bg-red-950/50 lg:w-3/5 mx-auto">
            <CardContent>
              <div className="flex items-center gap-2 text-red-800 dark:text-red-200">
                <XCircle className="h-5 w-5" />
                <span className="font-medium">Error</span>
              </div>
              <p className="mt-2 text-sm text-red-700 dark:text-red-300">{error}</p>
            </CardContent>
          </Card>
        )}

        {/* Domain Cards */}
        {!error && domains.length > 0 && (
          <div className="grid gap-6 md:grid-cols-2 lg:grid-cols-3">
            {filteredDomains.map((domain) => {
              const status = getDomainStatus(domain)
              return (
                <Card key={domain.name} className="overflow-hidden">
                  <CardHeader className="pb-2">
                    <div className="flex items-center justify-between">
                      <CardTitle className="text-lg">{domain.name}</CardTitle>
                      <Badge
                        variant={status === 'healthy' ? 'default' : status === 'warning' ? 'secondary' : 'destructive'}
                        className={cn(
                          'flex items-center gap-1',
                          status === 'healthy' &&
                            'bg-green-100 text-green-800 hover:bg-green-100 dark:bg-green-900/30 dark:text-green-400 dark:hover:bg-green-900/30',
                          status === 'warning' &&
                            'bg-yellow-100 text-yellow-800 hover:bg-yellow-100 dark:bg-yellow-900/30 dark:text-yellow-400 dark:hover:bg-yellow-900/30',
                          status === 'unhealthy' &&
                            'bg-red-100 text-red-800 hover:bg-red-100 dark:bg-red-900/30 dark:text-red-400 dark:hover:bg-red-900/30'
                        )}
                      >
                        {status === 'healthy' && <CheckCircle className="h-3 w-3" />}
                        {status === 'warning' && <AlertTriangle className="h-3 w-3" />}
                        {status === 'unhealthy' && <XCircle className="h-3 w-3" />}
                        {status.charAt(0).toUpperCase() + status.slice(1)}
                      </Badge>
                    </div>
                    <div className="flex items-center justify-between text-xs text-muted-foreground">
                      <span>Provider: {domain.status.provider}</span>
                      <span>
                        <Clock className="inline h-3 w-3 mr-1" />
                        {new Date(domain.status.lastUpdated).toLocaleString()}
                      </span>
                    </div>
                    <div className="text-sm text-muted-foreground">
                      {domain.status.endpoints.filter((e) => e.healthy).length}/{domain.status.endpoints.length}{' '}
                      endpoints healthy
                    </div>
                  </CardHeader>

                  <CardContent className="space-y-4">
                    {domain.status.error && (
                      <div className="rounded-lg bg-red-50 dark:bg-red-950/50 p-3 text-sm text-red-800 dark:text-red-200">
                        <div className="flex items-center gap-2">
                          <XCircle className="h-4 w-4" />
                          <span className="font-medium">Domain Error</span>
                        </div>
                        <p className="mt-1">{domain.status.error}</p>
                      </div>
                    )}

                    {/* Endpoints */}
                    {domain.status.endpoints.length > 0 && (
                      <div>
                        <h4 className="mb-2 text-sm font-medium">Endpoints ({domain.status.endpoints.length})</h4>
                        <div className="space-y-2">
                          {domain.status.endpoints.map((endpoint, index) => (
                            <div key={index} className="flex items-center justify-between rounded-lg border p-2">
                              <div className="flex items-center gap-2">
                                <Badge
                                  variant={endpoint.healthy ? 'default' : 'destructive'}
                                  className={cn(
                                    'text-xs',
                                    endpoint.healthy && 'bg-green-100 text-green-800 hover:bg-green-100',
                                    !endpoint.healthy && 'bg-red-100 text-red-800 hover:bg-red-100'
                                  )}
                                >
                                  {endpoint.healthy ? (
                                    <CheckCircle className="h-3 w-3" />
                                  ) : (
                                    <XCircle className="h-3 w-3" />
                                  )}
                                  {endpoint.healthy ? 'Healthy' : 'Unhealthy'}
                                </Badge>
                                <span className="font-mono text-sm">{endpoint.ip}</span>
                                {endpoint.inDns && (
                                  <Badge variant="outline" className="text-[10px] px-1.5 py-0 font-normal">
                                    DNS
                                  </Badge>
                                )}
                              </div>
                              <div className="text-right text-xs text-muted-foreground">
                                <div>Failures: {endpoint.failureCount || '0'}</div>
                              </div>
                            </div>
                          ))}
                        </div>
                      </div>
                    )}
                  </CardContent>
                </Card>
              )
            })}
          </div>
        )}

        {/* No Results Message */}
        {filteredDomains.length === 0 && searchTerm && !isLoading && (
          <div className="text-center py-8">
            <p className="text-muted-foreground">No domains or endpoints found matching "{searchTerm}"</p>
          </div>
        )}

        {/* No Domains Message */}
        {domains.length === 0 && !searchTerm && !isLoading && !error && (
          <div className="text-center py-8">
            <p className="text-muted-foreground">No domains configured or available</p>
          </div>
        )}

        {isLoading && (
          <div className="flex items-center justify-center py-8">
            <RefreshCw className="h-6 w-6 animate-spin" />
            <span className="ml-2">Loading domain data...</span>
          </div>
        )}
      </div>
    </div>
  )
}

export { DomainMonitorDashboard }
