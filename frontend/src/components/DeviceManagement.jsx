import React, { useState, useEffect, useImperativeHandle, forwardRef, useCallback } from 'react'
import { useForm } from 'react-hook-form'
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Card, CardContent, CardDescription, CardHeader } from "@/components/ui/card"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { AlertCircle, Plus, Trash2, RefreshCw, Copy, Check, Loader2, Settings, ChevronDown, ChevronUp } from "lucide-react"
import { FiltersSection } from "@/components/FiltersSection"
import { SortingSection } from "@/components/SortingSection"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"

// Device config form component - manages its own form state
function DeviceConfigForm({ username, initialFilters, initialSorting, onConfigChange, formRef }) {
  const form = useForm({
    defaultValues: {
      filters: initialFilters || {},
      sorting: initialSorting || {}
    }
  })

  const { watch, reset, getValues } = form
  const onConfigChangeRef = React.useRef(onConfigChange)
  const isInitialMount = React.useRef(true)

  // Expose form methods via ref so parent can get current values
  React.useImperativeHandle(formRef, formRef ? () => ({
    getValues: () => {
      // Get values with proper nesting - react-hook-form stores them nested
      const allValues = getValues()
      
      // Extract filters and sorting from the form values
      // The form structure is { filters: {...}, sorting: {...} }
      const filters = allValues.filters || {}
      const sorting = allValues.sorting || {}
      
      // Also check for any root-level fields that might have been incorrectly stored
      // (this shouldn't happen, but let's be defensive)
      const result = {
        filters: { ...filters },
        sorting: { ...sorting }
      }
      
      // If max_resolution is at root level, move it to filters
      if (allValues.max_resolution !== undefined && !result.filters.max_resolution) {
        result.filters.max_resolution = allValues.max_resolution
      }
      
      return result
    }
  }) : undefined, [getValues, username, formRef])

  // Keep ref updated
  useEffect(() => {
    onConfigChangeRef.current = onConfigChange
  }, [onConfigChange])

  // Update form when initial values change
  useEffect(() => {
    if (initialFilters || initialSorting) {
      const formData = {
        filters: initialFilters || {},
        sorting: initialSorting || {}
      }
      reset(formData, { keepDefaultValues: false })
      isInitialMount.current = true
    }
  }, [initialFilters, initialSorting, reset, username])

  // Watch for changes and notify parent - debounce to prevent excessive updates
  useEffect(() => {
    let timeoutId
    const subscription = watch((value) => {
      // Skip initial mount
      if (isInitialMount.current) {
        isInitialMount.current = false
        return
      }
      
      if (value && (value.filters || value.sorting)) {
        // Debounce updates to prevent flickering
        clearTimeout(timeoutId)
        timeoutId = setTimeout(() => {
          onConfigChangeRef.current(username, value)
        }, 100)
      }
    })
    return () => {
      clearTimeout(timeoutId)
      subscription.unsubscribe()
    }
  }, [watch, username])

  return (
    <Tabs defaultValue="filters" className="w-full">
      <TabsList className="mb-6">
        <TabsTrigger value="filters">Filters</TabsTrigger>
        <TabsTrigger value="sorting">Sorting</TabsTrigger>
      </TabsList>
      <TabsContent value="filters">
        <FiltersSection 
          control={form.control}
          watch={watch}
          fieldPrefix="filters"
        />
      </TabsContent>
      <TabsContent value="sorting">
        <SortingSection 
          control={form.control}
          watch={watch}
          fieldPrefix="sorting"
        />
      </TabsContent>
    </Tabs>
  )
}

const DeviceManagement = forwardRef(function DeviceManagement({ globalFilters, globalSorting, sendCommand, ws }, ref) {
  const [devices, setDevices] = useState([])
  const [loading, setLoading] = useState(true) // Start as true to show loader initially
  const [actionLoading, setActionLoading] = useState(null) // Track which action is loading
  const [addDeviceLoading, setAddDeviceLoading] = useState(false) // Separate loading for add device dialog
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [showAddDialog, setShowAddDialog] = useState(false)
  const [expandedDevice, setExpandedDevice] = useState(null)
  const [newUsername, setNewUsername] = useState('')
  const [copiedToken, setCopiedToken] = useState('')
  const [globalConfig, setGlobalConfig] = useState(null)
  
  // Store device configs - keyed by username
  const [deviceConfigs, setDeviceConfigs] = useState({})
  // Store form refs for each device so we can get current values directly
  const formRefs = React.useRef({})
  // Track if initial load has happened
  const hasLoadedRef = React.useRef(false)

  // Expose getDeviceConfigs to parent via ref
  useImperativeHandle(ref, () => ({
    getDeviceConfigs: () => {
      // Get current values directly from forms to ensure we have the latest data
      const configs = {}
      
      for (const [username, formRef] of Object.entries(formRefs.current)) {
        if (formRef && formRef.current && formRef.current.getValues) {
          const formValues = formRef.current.getValues()
          if (formValues && (formValues.filters || formValues.sorting)) {
            configs[username] = formValues
          }
        } else if (deviceConfigs[username]) {
          // Fallback to state if form ref not available
          configs[username] = deviceConfigs[username]
        }
      }
      return configs
    },
    // Backwards compatibility alias
    getUserConfigs: () => {
      const configs = {}
      for (const [username, formRef] of Object.entries(formRefs.current)) {
        if (formRef && formRef.current && formRef.current.getValues) {
          const formValues = formRef.current.getValues()
          if (formValues && (formValues.filters || formValues.sorting)) {
            configs[username] = formValues
          }
        } else if (deviceConfigs[username]) {
          configs[username] = deviceConfigs[username]
        }
      }
      return configs
    }
  }))

  // Fetch devices list
  const fetchDevices = useCallback((showLoader = true) => {
    if (!sendCommand || !ws || ws.readyState !== WebSocket.OPEN) {
      setError('WebSocket not connected')
      if (showLoader) {
        setLoading(false)
      }
      return
    }

    if (showLoader) {
      setLoading(true)
    }
    setError('')
    
    // Clean up any existing callback
    if (window.deviceManagementCallback) {
      delete window.deviceManagementCallback
    }
    
    window.deviceManagementCallback = (payload) => {
      if (payload.error) {
        setError(payload.error)
        if (showLoader) {
          setLoading(false)
        }
        delete window.deviceManagementCallback
        return
      }
      setDevices(payload)
      if (showLoader) {
        setLoading(false)
      }
      hasLoadedRef.current = true
      delete window.deviceManagementCallback
    }

    sendCommand('get_users', {})
  }, [sendCommand, ws])

  // Fetch global config
  const fetchGlobalConfig = useCallback(() => {
    if (sendCommand && ws && ws.readyState === WebSocket.OPEN) {
      // Clean up any existing callback
      if (window.globalConfigCallback) {
        delete window.globalConfigCallback
      }
      window.globalConfigCallback = (config) => {
        setGlobalConfig(config)
        delete window.globalConfigCallback
      }
      sendCommand('get_config', {})
    }
  }, [sendCommand, ws])

  // Initial load - only run once when component mounts
  useEffect(() => {
    if (!hasLoadedRef.current && ws && ws.readyState === WebSocket.OPEN) {
      fetchDevices(true)
      fetchGlobalConfig()
    }
  }, []) // Empty deps - only run on mount

  // Also fetch when WebSocket becomes available
  useEffect(() => {
    if (!hasLoadedRef.current && ws && ws.readyState === WebSocket.OPEN && sendCommand) {
      fetchDevices(true)
      fetchGlobalConfig()
    }
  }, [ws?.readyState, sendCommand]) // Only depend on WebSocket state

  // Track loaded devices to avoid re-fetching
  const loadedDevicesRef = React.useRef(new Set())

  // Load device config when expanded
  const loadDeviceConfig = useCallback((username) => {
    if (loadedDevicesRef.current.has(username)) {
      return // Already loaded
    }

    if (!sendCommand || !ws || ws.readyState !== WebSocket.OPEN) {
      // Use defaults if WebSocket not available
      const defaultConfig = {
        filters: globalFilters || globalConfig?.filters || {},
        sorting: globalSorting || globalConfig?.sorting || {}
      }
        setDeviceConfigs(prev => ({ ...prev, [username]: defaultConfig }))
        loadedDevicesRef.current.add(username)
        // Create form ref if it doesn't exist
        if (!formRefs.current[username]) {
          formRefs.current[username] = React.createRef()
        }
        return
      }

      window.deviceResponseCallback = (payload) => {
        const configData = payload.error ? {
          filters: globalFilters || globalConfig?.filters || {},
          sorting: globalSorting || globalConfig?.sorting || {}
        } : {
          // Use device's saved config directly, don't merge with defaults
          // The backend already returns the device's filters/sorting
          filters: payload.filters || {},
          sorting: payload.sorting || {}
        }
        
        setDeviceConfigs(prev => ({ ...prev, [username]: configData }))
        loadedDevicesRef.current.add(username)
        // Create form ref if it doesn't exist
        if (!formRefs.current[username]) {
          formRefs.current[username] = React.createRef()
        }
        delete window.deviceResponseCallback
      }

    sendCommand('get_user', { username })
  }, [sendCommand, ws, globalFilters, globalSorting, globalConfig])

  // Handle toggle config expansion
  const handleToggleConfig = useCallback((username) => {
    setExpandedDevice(prev => {
      if (prev === username) {
        return null
      } else {
        loadDeviceConfig(username)
        return username
      }
    })
  }, [loadDeviceConfig])

  // Handle config changes
  const handleConfigChange = useCallback((username, config) => {
    setDeviceConfigs(prev => ({
      ...prev,
      [username]: config
    }))
  }, [])

  // Handle add device
  const handleAddDevice = async (e) => {
    e.preventDefault()
    e.stopPropagation()
    setError('')
    setSuccess('')
    setAddDeviceLoading(true)

    if (!sendCommand || !ws || ws.readyState !== WebSocket.OPEN) {
      setError('WebSocket not connected')
      setAddDeviceLoading(false)
      return
    }

    // Clean up any existing callback
    if (window.deviceActionCallback) {
      delete window.deviceActionCallback
    }

    window.deviceActionCallback = (payload) => {
      setAddDeviceLoading(false)
      if (payload.error) {
        setError(payload.error)
      } else {
        setSuccess(`Device "${newUsername}" created successfully`)
        setNewUsername('')
        setShowAddDialog(false)
        // Refresh list without showing loader (silent refresh)
        fetchDevices(false)
      }
      delete window.deviceActionCallback
    }

    sendCommand('create_user', { username: newUsername })
  }

  // Handle delete device
  const handleDeleteDevice = (username) => {
    if (username === 'admin') {
      setError('Cannot delete admin device')
      return
    }

    if (!confirm(`Are you sure you want to delete device "${username}"?`)) {
      return
    }

    if (!sendCommand || !ws || ws.readyState !== WebSocket.OPEN) {
      setError('WebSocket not connected')
      return
    }

    setError('')
    setSuccess('')
    setActionLoading(`delete-${username}`)

    // Clean up any existing callback
    if (window.deviceActionCallback) {
      delete window.deviceActionCallback
    }

    window.deviceActionCallback = (payload) => {
      setActionLoading(null)
      if (payload.error) {
        setError(payload.error)
      } else {
        setSuccess(`Device "${username}" deleted successfully`)
        // Clean up configs
        setDeviceConfigs(prev => {
          const next = { ...prev }
          delete next[username]
          return next
        })
        loadedDevicesRef.current.delete(username)
        delete formRefs.current[username]
        setExpandedDevice(prev => prev === username ? null : prev)
        // Refresh list without showing loader (silent refresh)
        fetchDevices(false)
      }
      delete window.deviceActionCallback
    }

    sendCommand('delete_user', { username })
  }

  // Handle regenerate token
  const handleRegenerateToken = (username) => {
    if (!sendCommand || !ws || ws.readyState !== WebSocket.OPEN) {
      setError('WebSocket not connected')
      return
    }

    setError('')
    setSuccess('')
    setActionLoading(`regenerate-${username}`)

    // Clean up any existing callback
    if (window.deviceActionCallback) {
      delete window.deviceActionCallback
    }

    window.deviceActionCallback = (payload) => {
      setActionLoading(null)
      if (payload.error) {
        setError(payload.error)
      } else {
        setSuccess(`Token regenerated for "${username}"`)
        setDevices(prev => prev.map(d => d.username === username ? { ...d, token: payload.token } : d))
      }
      delete window.deviceActionCallback
    }

    sendCommand('regenerate_token', { username })
  }


  // Get manifest URL
  const getManifestUrl = (token) => {
    const baseUrl = globalConfig?.addon_base_url 
      ? globalConfig.addon_base_url.replace(/\/$/, '')
      : window.location.origin
    return `${baseUrl}/${token}/manifest.json`
  }

  // Copy manifest URL
  const copyManifestUrl = (token) => {
    const url = getManifestUrl(token)
    navigator.clipboard.writeText(url)
    setCopiedToken(token)
    setTimeout(() => setCopiedToken(''), 2000)
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <div>
            <CardDescription>
              Manage devices and their authentication tokens
            </CardDescription>
          </div>
          <Dialog open={showAddDialog} onOpenChange={setShowAddDialog}>
            <DialogTrigger asChild>
              <Button type="button">
                <Plus className="mr-2 h-4 w-4" />
                Add Device
              </Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Add New Device</DialogTitle>
                <DialogDescription>
                  Create a new device account. Devices will access Stremio via their token in the URL.
                </DialogDescription>
              </DialogHeader>
              <form onSubmit={handleAddDevice} className="space-y-4">
                {error && (
                  <div className="flex items-center gap-2 p-3 text-sm text-destructive bg-destructive/10 rounded-md">
                    <AlertCircle className="h-4 w-4" />
                    <span>{error}</span>
                  </div>
                )}
                {success && (
                  <div className="flex items-center gap-2 p-3 text-sm text-green-600 bg-green-50 rounded-md">
                    <Check className="h-4 w-4" />
                    <span>{success}</span>
                  </div>
                )}
                <div className="space-y-2">
                  <Label htmlFor="new-username">Username</Label>
                  <Input
                    id="new-username"
                    type="text"
                    placeholder="Enter username"
                    value={newUsername}
                    onChange={(e) => setNewUsername(e.target.value)}
                    required
                    disabled={addDeviceLoading}
                  />
                  <p className="text-xs text-muted-foreground">
                    Devices access Stremio via their token in the URL: /{`{token}`}/manifest.json
                  </p>
                </div>
                <Button type="submit" className="w-full" disabled={addDeviceLoading}>
                  {addDeviceLoading ? (
                    <>
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                      Creating...
                    </>
                  ) : (
                    'Create Device'
                  )}
                </Button>
              </form>
            </DialogContent>
          </Dialog>
        </div>
      </CardHeader>
      <CardContent>
        {error && !showAddDialog && (
          <div className="flex items-center gap-2 p-3 mb-4 text-sm text-destructive bg-destructive/10 rounded-md">
            <AlertCircle className="h-4 w-4" />
            <span>{error}</span>
          </div>
        )}
        {success && !showAddDialog && (
          <div className="flex items-center gap-2 p-3 mb-4 text-sm text-green-600 bg-green-50 rounded-md">
            <Check className="h-4 w-4" />
            <span>{success}</span>
          </div>
        )}

        {loading ? (
          <div className="flex items-center justify-center p-8">
            <Loader2 className="h-6 w-6 animate-spin" />
          </div>
        ) : devices.length === 0 ? (
          <div className="text-center p-8 text-muted-foreground">
            No devices found. Create your first device to get started.
          </div>
        ) : (
          <div className="space-y-4">
            {devices.map((device) => (
              <Card key={device.username}>
                <CardContent className="pt-6">
                  <div className="space-y-4">
                    <div className="flex items-start justify-between">
                      <div className="flex-1">
                        <div className="flex items-center gap-2 mb-2">
                          <h3 className="font-semibold">{device.username}</h3>
                        </div>
                        <div className="space-y-2">
                          <div className="flex items-center gap-2">
                            <Label className="text-xs text-muted-foreground">Stremio URL:</Label>
                            <code className="text-xs bg-muted px-2 py-1 rounded flex-1 truncate">
                              {getManifestUrl(device.token)}
                            </code>
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              onClick={() => copyManifestUrl(device.token)}
                              className="h-7"
                              title="Copy manifest URL"
                            >
                              {copiedToken === device.token ? (
                                <Check className="h-3 w-3" />
                              ) : (
                                <Copy className="h-3 w-3" />
                              )}
                            </Button>
                          </div>
                        </div>
                      </div>
                      <div className="flex gap-2 ml-4">
                        {device.username !== 'admin' && (
                          <Button
                            type="button"
                            variant={expandedDevice === device.username ? "default" : "outline"}
                            size="sm"
                            onClick={() => handleToggleConfig(device.username)}
                            disabled={actionLoading !== null || loading}
                          >
                            {expandedDevice === device.username ? (
                              <>
                                <ChevronUp className="h-3 w-3 mr-1" />
                                Hide Config
                              </>
                            ) : (
                              <>
                                <Settings className="h-3 w-3 mr-1" />
                                Configure
                              </>
                            )}
                          </Button>
                        )}
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          onClick={() => handleRegenerateToken(device.username)}
                          disabled={actionLoading !== null || loading}
                        >
                          {actionLoading === `regenerate-${device.username}` ? (
                            <Loader2 className="h-3 w-3 mr-1 animate-spin" />
                          ) : (
                            <RefreshCw className="h-3 w-3 mr-1" />
                          )}
                          Regenerate Token
                        </Button>
                        {device.username !== 'admin' && (
                          <Button
                            type="button"
                            variant="destructive"
                            size="sm"
                            onClick={() => handleDeleteDevice(device.username)}
                            disabled={actionLoading !== null || loading}
                          >
                            {actionLoading === `delete-${device.username}` ? (
                              <Loader2 className="h-3 w-3 animate-spin" />
                            ) : (
                              <Trash2 className="h-3 w-3" />
                            )}
                          </Button>
                        )}
                      </div>
                    </div>
                    
                    {device.username !== 'admin' && expandedDevice === device.username && deviceConfigs[device.username] && (() => {
                      // Ensure form ref exists for this device
                      if (!formRefs.current[device.username]) {
                        formRefs.current[device.username] = React.createRef()
                      }
                      return (
                        <div className="pt-4 border-t">
                          <DeviceConfigForm 
                            username={device.username}
                            initialFilters={deviceConfigs[device.username]?.filters}
                            initialSorting={deviceConfigs[device.username]?.sorting}
                            onConfigChange={handleConfigChange}
                            formRef={formRefs.current[device.username]}
                          />
                        </div>
                      )
                    })()}
                  </div>
                </CardContent>
              </Card>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  )
})

export default DeviceManagement
