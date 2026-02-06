import React, { useEffect, useState } from 'react'
import { useForm, useFieldArray } from 'react-hook-form'
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, DialogDescription } from "@/components/ui/dialog"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Separator } from "@/components/ui/separator"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Form, FormField, FormItem, FormLabel, FormControl, FormMessage } from "@/components/ui/form"
import { PasswordInput } from "@/components/ui/password-input"
import { Trash2, Plus, Loader2, RotateCcw } from "lucide-react"

function Settings({ initialConfig, sendCommand, saveStatus, isSaving, onClose }) {
  const [loading, setLoading] = useState(!initialConfig)

  const form = useForm({
    defaultValues: {
      nzbhydra_url: '',
      nzbhydra_api_key: '',
      prowlarr_url: '',
      prowlarr_api_key: '',
      addon_port: 7000,
      addon_base_url: '',
      log_level: 'INFO',
      security_token: '',
      proxy_enabled: false,
      proxy_port: 119,
      proxy_host: '',
      proxy_auth_user: '',
      proxy_auth_pass: '',
      cache_ttl_seconds: 300,
      validation_sample_size: 5,
      max_concurrent_validations: 20,
      providers: []
    }
  })

  // Destructure for easy access
  const { control, handleSubmit, reset, setError, formState } = form
  const { fields, append, remove } = useFieldArray({
    control,
    name: 'providers'
  })

  useEffect(() => {
    if (initialConfig) {
      const formattedData = {
        ...initialConfig,
        addon_port: Number(initialConfig.addon_port),
        proxy_port: Number(initialConfig.proxy_port),
        cache_ttl_seconds: Number(initialConfig.cache_ttl_seconds),
        validation_sample_size: Number(initialConfig.validation_sample_size),
        max_concurrent_validations: Number(initialConfig.max_concurrent_validations),
        providers: initialConfig.providers?.map(p => ({
          ...p,
          port: Number(p.port),
          connections: Number(p.connections)
        })) || []
      }
      reset(formattedData)
      setLoading(false)
    }
  }, [initialConfig, reset])

  // Map backend errors to form fields
  useEffect(() => {
      if (saveStatus.errors) {
          Object.keys(saveStatus.errors).forEach(key => {
              setError(key, { type: 'server', message: saveStatus.errors[key] });
          });
      }
  }, [saveStatus.errors, setError]);

  const onSubmit = async (data) => {
    sendCommand('save_config', data)
  }

  if (loading) return null
  
  return (
    <Dialog open={true} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-[90vw] h-[90vh] flex flex-col p-0 gap-0 overflow-hidden">
        <DialogHeader className="p-6 pb-4 border-b shrink-0">
          <DialogTitle>Configuration</DialogTitle>
          <DialogDescription>
            Configure your indexers and Usenet providers.
          </DialogDescription>
        </DialogHeader>

        <ScrollArea className="flex-1 min-h-0 w-full">
            <div className="p-6">
                <Form {...form}>
                    <form onSubmit={handleSubmit(onSubmit)} className="space-y-6">
                        
                        {/* Indexers Section */}
                        <Card>
                            <CardHeader>
                                <CardTitle className="text-lg">Indexers</CardTitle>
                                <CardDescription>Configure Prowlarr or NZBHydra2 connection details.</CardDescription>
                            </CardHeader>
                            <CardContent className="grid gap-4">
                                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                                    <FormField
                                        control={control}
                                        name="nzbhydra_url"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>NZBHydra2 URL</FormLabel>
                                                <FormControl>
                                                    <Input placeholder="http://localhost:5076" {...field} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                    <FormField
                                        control={control}
                                        name="nzbhydra_api_key"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>NZBHydra2 API Key</FormLabel>
                                                <FormControl>
                                                    <PasswordInput {...field} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                </div>
                                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                                    <FormField
                                        control={control}
                                        name="prowlarr_url"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>Prowlarr URL</FormLabel>
                                                <FormControl>
                                                    <Input placeholder="http://localhost:9696" {...field} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                    <FormField
                                        control={control}
                                        name="prowlarr_api_key"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>Prowlarr API Key</FormLabel>
                                                <FormControl>
                                                     <PasswordInput {...field} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                </div>
                            </CardContent>
                        </Card>

                        {/* Addon Settings */}
                        <Card>
                            <CardHeader>
                                <CardTitle className="text-lg">Addon Settings</CardTitle>
                                <CardDescription>Configure how the Stremio addon listens and is accessed.</CardDescription>
                            </CardHeader>
                            <CardContent className="grid gap-4">
                                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                                    <FormField
                                        control={control}
                                        name="addon_base_url"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>Base URL</FormLabel>
                                                <FormControl>
                                                    <Input placeholder="http://localhost:7000" {...field} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                    <FormField
                                        control={control}
                                        name="addon_port"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>Port (Requires Restart)</FormLabel>
                                                <FormControl>
                                                    <Input type="number" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                </div>
                                <FormField
                                    control={control}
                                    name="security_token"
                                    render={({ field }) => (
                                        <FormItem>
                                            <FormLabel>Security Token (Requires Restart)</FormLabel>
                                            <FormControl>
                                                <PasswordInput placeholder="Secure secret path" {...field} />
                                            </FormControl>
                                            <FormMessage />
                                        </FormItem>
                                    )}
                                />
                            </CardContent>
                        </Card>

                        {/* NNTP Proxy Settings */}
                        {/* ... (no changes here) ... */}
                        
                        {/* ... (rest of form) ... */}
                        
                        <Card>
                            <CardHeader>
                                <CardTitle className="text-lg">NNTP Proxy Server</CardTitle>
                                <CardDescription>Allow other apps (SABnzbd, NBZGet) to use StreamNZB as a localized news server.</CardDescription>
                            </CardHeader>
                            <CardContent className="grid gap-4">
                                <FormField
                                    control={control}
                                    name="proxy_enabled"
                                    render={({ field }) => (
                                        <FormItem className="flex flex-row items-center justify-between rounded-lg border p-4">
                                            <div className="space-y-0.5">
                                                <FormLabel className="text-base">Enable Proxy</FormLabel>
                                            </div>
                                            <FormControl>
                                                <Checkbox
                                                    checked={field.value}
                                                    onCheckedChange={field.onChange}
                                                />
                                            </FormControl>
                                        </FormItem>
                                    )}
                                />
                                {form.watch('proxy_enabled') && (
                                    <>
                                        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                                            <FormField
                                                control={control}
                                                name="proxy_host"
                                                render={({ field }) => (
                                                    <FormItem>
                                                        <FormLabel>Bind Host</FormLabel>
                                                        <FormControl>
                                                            <Input placeholder="0.0.0.0" {...field} />
                                                        </FormControl>
                                                        <FormMessage />
                                                    </FormItem>
                                                )}
                                            />
                                            <FormField
                                                control={control}
                                                name="proxy_port"
                                                render={({ field }) => (
                                                    <FormItem>
                                                        <FormLabel>Port</FormLabel>
                                                        <FormControl>
                                                            <Input type="number" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} />
                                                        </FormControl>
                                                        <FormMessage />
                                                    </FormItem>
                                                )}
                                            />
                                        </div>
                                        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                                            <FormField
                                                control={control}
                                                name="proxy_auth_user"
                                                render={({ field }) => (
                                                    <FormItem>
                                                        <FormLabel>Proxy Username</FormLabel>
                                                        <FormControl>
                                                            <Input {...field} />
                                                        </FormControl>
                                                        <FormMessage />
                                                    </FormItem>
                                                )}
                                            />
                                            <FormField
                                                control={control}
                                                name="proxy_auth_pass"
                                                render={({ field }) => (
                                                    <FormItem>
                                                        <FormLabel>Proxy Password</FormLabel>
                                                        <FormControl>
                                                            <PasswordInput {...field} />
                                                        </FormControl>
                                                        <FormMessage />
                                                    </FormItem>
                                                )}
                                            />
                                        </div>
                                    </>
                                )}
                            </CardContent>
                        </Card>
                        <Card>
                            <CardHeader>
                                <CardTitle className="text-lg">Advanced Settings</CardTitle>
                            </CardHeader>
                            <CardContent>
                                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                                     <FormField
                                        control={control}
                                        name="log_level"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>Log Level</FormLabel>
                                                <div className="relative w-full">
                                                    <select 
                                                        className="flex h-10 w-full items-center justify-between rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
                                                        {...field}
                                                    >
                                                        <option value="DEBUG">DEBUG</option>
                                                        <option value="INFO">INFO</option>
                                                        <option value="WARN">WARN</option>
                                                        <option value="ERROR">ERROR</option>
                                                    </select>
                                                </div>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                    <FormField
                                        control={control}
                                        name="cache_ttl_seconds"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>Validation Cache TTL (Seconds)</FormLabel>
                                                <FormControl>
                                                    <Input type="number" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                </div>
                                <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mt-4">
                                    <FormField
                                        control={control}
                                        name="validation_sample_size"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>Validation Sample Size</FormLabel>
                                                <FormControl>
                                                    <Input type="number" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                    <FormField
                                        control={control}
                                        name="max_concurrent_validations"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>Max Concurrent Validations</FormLabel>
                                                <FormControl>
                                                    <Input type="number" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                </div>
                            </CardContent>
                        </Card>

                        {/* Providers Section */}
                       
                        <div>
                            <div className="flex items-center justify-between mb-4">
                                <h3 className="text-lg font-medium">Usenet Providers</h3>
                                <Button type="button" variant="outline" size="sm" onClick={() => append({ host: '', port: 563, username: '', password: '', connections: 30, use_ssl: true })}>
                                    <Plus className="w-4 h-4 mr-2" /> Add Provider
                                </Button>
                            </div>
                            <div className="grid gap-4">
                                {fields.map((field, index) => (
                                    <Card key={field.id} className="relative">
                                        <Button
                                            type="button"
                                            variant="ghost"
                                            size="icon"
                                            className="absolute right-2 top-2 h-8 w-8 text-destructive hover:text-destructive/90"
                                            onClick={() => remove(index)}
                                        >
                                            <Trash2 className="h-4 w-4" />
                                        </Button>
                                        <CardHeader className="pb-3">
                                            <CardTitle className="text-base">Provider {index + 1}</CardTitle>
                                        </CardHeader>
                                        <CardContent className="grid gap-4">
                                            <FormField
                                                control={control}
                                                name={`providers.${index}.name`}
                                                render={({ field }) => (
                                                    <FormItem>
                                                        <FormLabel>Provider Name (Optional)</FormLabel>
                                                        <FormControl><Input placeholder="e.g. Newshosting, Eweka" {...field} /></FormControl>
                                                        <FormMessage />
                                                    </FormItem>
                                                )}
                                            />
                                            <div className="grid grid-cols-2 gap-4">
                                                <FormField
                                                    control={control}
                                                    name={`providers.${index}.host`}
                                                    render={({ field }) => (
                                                        <FormItem>
                                                            <FormLabel>Host</FormLabel>
                                                            <FormControl><Input placeholder="news.example.com" {...field} /></FormControl>
                                                            <FormMessage />
                                                        </FormItem>
                                                    )}
                                                />
                                                <FormField
                                                    control={control}
                                                    name={`providers.${index}.port`}
                                                    render={({ field }) => (
                                                        <FormItem>
                                                            <FormLabel>Port</FormLabel>
                                                            <FormControl><Input type="number" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} /></FormControl>
                                                            <FormMessage />
                                                        </FormItem>
                                                    )}
                                                />
                                            </div>
                                            <div className="grid grid-cols-2 gap-4">
                                                <FormField
                                                    control={control}
                                                    name={`providers.${index}.username`}
                                                    render={({ field }) => (
                                                        <FormItem>
                                                            <FormLabel>Username</FormLabel>
                                                            <FormControl><Input {...field} /></FormControl>
                                                            <FormMessage />
                                                        </FormItem>
                                                    )}
                                                />
                                                 <FormField
                                                    control={control}
                                                    name={`providers.${index}.password`}
                                                    render={({ field }) => (
                                                        <FormItem>
                                                            <FormLabel>Password</FormLabel>
                                                            <FormControl><PasswordInput {...field} /></FormControl>
                                                            <FormMessage />
                                                        </FormItem>
                                                    )}
                                                />
                                            </div>
                                            <div className="flex items-end gap-6">
                                                 <FormField
                                                    control={control}
                                                    name={`providers.${index}.connections`}
                                                    render={({ field }) => (
                                                        <FormItem className="w-32">
                                                            <FormLabel>Connections</FormLabel>
                                                            <FormControl><Input type="number" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} /></FormControl>
                                                            <FormMessage />
                                                        </FormItem>
                                                    )}
                                                />
                                                 <FormField
                                                    control={control}
                                                    name={`providers.${index}.use_ssl`}
                                                    render={({ field }) => (
                                                        <FormItem className="flex flex-row items-start space-x-3 space-y-0 rounded-md border p-4 h-[42px] items-center">
                                                            <FormControl>
                                                                <Checkbox
                                                                    checked={field.value}
                                                                    onCheckedChange={field.onChange}
                                                                />
                                                            </FormControl>
                                                            <div className="space-y-1 leading-none">
                                                                <FormLabel>
                                                                    Use SSL
                                                                </FormLabel>
                                                            </div>
                                                        </FormItem>
                                                    )}
                                                />
                                            </div>
                                        </CardContent>
                                    </Card>
                                ))}
                            </div>
                        </div>
                    </form>
                </Form>
            </div>
        </ScrollArea>

        <DialogFooter className="p-6 pt-4 border-t gap-2 sm:justify-between shrink-0">
           <div className={`flex items-center text-sm ${saveStatus.type === 'error' ? 'text-destructive' : saveStatus.type === 'success' ? 'text-green-500' : 'text-muted-foreground'}`}>
              {saveStatus.msg}
           </div>
           <div className="flex gap-2">
              <Button type="button" variant="destructive" onClick={() => {
                  if (confirm('Are you sure you want to restart StreamNZB?')) {
                      // Calculate new URL based on current form values (which should be saved)
                      const currentPort = window.location.port || (window.location.protocol === 'https:' ? '443' : '80');
                      const newPort = form.getValues('addon_port').toString();
                      const newToken = form.getValues('security_token');
                      
                      // Check if we need to redirect
                      // Note: We use existing hostname. 
                      // If security token is set, path should start with /token/
                      // If empty, path is /
                      
                      const hostname = window.location.hostname;
                      const protocol = window.location.protocol;
                      
                      let newPath = '/';
                      if (newToken) {
                          newPath = `/${newToken}/`;
                      }
                      
                      const newUrl = `${protocol}//${hostname}:${newPort}${newPath}`;
                      
                      sendCommand('restart', {})
                      
                      // If URL changed, redirect after a delay
                      if (newPort !== currentPort || !window.location.pathname.startsWith(newPath)) {
                           // Use a slightly longer timeout to allow the backend to receive the command and die
                           // and hopefully start coming back up.
                          setTimeout(() => {
                              window.location.href = newUrl;
                          }, 3000); 
                      }
                  }
              }}>
                 <RotateCcw className="mr-2 h-4 w-4" />
                 Restart App
              </Button>
              <Button type="button" onClick={handleSubmit(onSubmit)} disabled={isSaving || formState.isSubmitting}>
                 {(isSaving || formState.isSubmitting) && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
                 Save Changes
              </Button>
           </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export default Settings


