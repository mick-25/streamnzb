import React, { useMemo } from 'react'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import { FormField, FormItem, FormLabel, FormControl, FormMessage } from "@/components/ui/form"
import { PasswordInput } from "@/components/ui/password-input"
import { Trash2, Plus } from "lucide-react"

export function ProviderSettings({ control, fields, append, remove, watch }) {
  // Sort providers by priority (lower number = higher priority)
  const sortedFields = useMemo(() => {
    return [...fields].sort((a, b) => {
      const priorityA = watch(`providers.${a.id}.priority`) ?? 999
      const priorityB = watch(`providers.${b.id}.priority`) ?? 999
      return priorityA - priorityB
    })
  }, [fields, watch])

  return (
    <div className="space-y-6">
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            {sortedFields.map((field) => {
              const index = fields.findIndex(f => f.id === field.id)
              return (
                <Card key={field.id} className="relative flex flex-col h-full">
                    <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="absolute right-1 top-1 h-8 w-8 text-destructive hover:text-destructive/90 z-10"
                        onClick={() => remove(index)}
                    >
                        <Trash2 className="h-4 w-4" />
                    </Button>
                    <CardHeader className="pb-3">
                        <CardTitle className="text-base truncate pr-8">
                            {watch(`providers.${index}.name`) || `Provider ${index + 1}`}
                            {watch(`providers.${index}.priority`) != null && (
                              <span className="ml-2 text-xs text-muted-foreground">
                                (Priority: {watch(`providers.${index}.priority`)})
                              </span>
                            )}
                        </CardTitle>
                    </CardHeader>
                    <CardContent className="space-y-4 flex-grow px-4 pb-4">
                        <div className="flex items-center gap-4">
                            <FormField
                                control={control}
                                name={`providers.${index}.enabled`}
                                render={({ field }) => (
                                    <FormItem className="flex flex-row items-center space-x-2 space-y-0">
                                        <FormControl>
                                            <Checkbox
                                                checked={field.value != null ? field.value : true}
                                                onCheckedChange={field.onChange}
                                            />
                                        </FormControl>
                                        <FormLabel className="text-xs">Enabled</FormLabel>
                                    </FormItem>
                                )}
                            />
                            <FormField
                                control={control}
                                name={`providers.${index}.priority`}
                                render={({ field }) => (
                                    <FormItem className="w-24">
                                        <FormLabel className="text-xs">Priority</FormLabel>
                                        <FormControl>
                                            <Input 
                                                type="number" 
                                                className="h-8 text-xs" 
                                                placeholder="1"
                                                {...field} 
                                                onChange={e => field.onChange(e.target.valueAsNumber || 1)} 
                                            />
                                        </FormControl>
                                        <FormMessage />
                                    </FormItem>
                                )}
                            />
                        </div>
                        <FormField
                            control={control}
                            name={`providers.${index}.name`}
                            render={({ field }) => (
                                <FormItem>
                                    <FormLabel className="text-xs">Provider Name (Optional)</FormLabel>
                                    <FormControl><Input placeholder="e.g. Newshosting" className="h-8 text-xs" {...field} /></FormControl>
                                    <FormMessage />
                                </FormItem>
                            )}
                        />
                        <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                            <FormField
                                control={control}
                                name={`providers.${index}.host`}
                                render={({ field }) => (
                                    <FormItem>
                                        <FormLabel className="text-xs">Host</FormLabel>
                                        <FormControl><Input placeholder="news.example.com" className="h-8 text-xs" {...field} /></FormControl>
                                        <FormMessage />
                                    </FormItem>
                                )}
                            />
                            <FormField
                                control={control}
                                name={`providers.${index}.port`}
                                render={({ field }) => (
                                    <FormItem>
                                        <FormLabel className="text-xs">Port</FormLabel>
                                        <FormControl><Input type="number" className="h-8 text-xs" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} /></FormControl>
                                        <FormMessage />
                                    </FormItem>
                                )}
                            />
                        </div>
                        <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                            <FormField
                                control={control}
                                name={`providers.${index}.username`}
                                render={({ field }) => (
                                    <FormItem>
                                        <FormLabel className="text-xs">Username</FormLabel>
                                        <FormControl><Input className="h-8 text-xs" {...field} /></FormControl>
                                        <FormMessage />
                                    </FormItem>
                                )}
                            />
                             <FormField
                                control={control}
                                name={`providers.${index}.password`}
                                render={({ field }) => (
                                    <FormItem>
                                        <FormLabel className="text-xs">Password</FormLabel>
                                        <FormControl><PasswordInput className="h-8 text-xs" {...field} /></FormControl>
                                        <FormMessage />
                                    </FormItem>
                                )}
                            />
                        </div>
                        <div className="flex items-center gap-4">
                             <FormField
                                control={control}
                                name={`providers.${index}.connections`}
                                render={({ field }) => (
                                    <FormItem className="w-20">
                                        <FormLabel className="text-xs">Conns</FormLabel>
                                        <FormControl><Input type="number" className="h-8 text-xs" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} /></FormControl>
                                        <FormMessage />
                                    </FormItem>
                                )}
                            />
                             <FormField
                                control={control}
                                name={`providers.${index}.use_ssl`}
                                render={({ field }) => (
                                    <FormItem className="flex flex-row items-center space-x-2 space-y-0 h-8 mt-6">
                                        <FormControl>
                                            <Checkbox
                                                checked={field.value}
                                                onCheckedChange={field.onChange}
                                            />
                                        </FormControl>
                                        <FormLabel className="text-xs">Use SSL</FormLabel>
                                    </FormItem>
                                )}
                            />
                        </div>
                    </CardContent>
                </Card>
              )
            })}

            {/* Skeleton Add Card */}
            <button
                type="button"
                onClick={() => {
                  const priorities = fields.map((_, i) => {
                    const p = watch(`providers.${i}.priority`)
                    return p != null && p > 0 ? p : 0
                  })
                  const maxPriority = priorities.length > 0 ? Math.max(...priorities, 0) : 0
                  append({ 
                    host: '', 
                    port: 563, 
                    username: '', 
                    password: '', 
                    connections: 30, 
                    use_ssl: true,
                    priority: maxPriority + 1,
                    enabled: true
                  })
                }}
                className="flex flex-col items-center justify-center p-6 border-2 border-dashed rounded-lg border-muted-foreground/25 hover:border-muted-foreground/50 hover:bg-accent/50 transition-all min-h-[250px] group"
            >
                <div className="flex items-center justify-center w-12 h-12 rounded-full bg-primary/10 group-hover:bg-primary/20 transition-colors mb-4">
                    <Plus className="w-6 h-6 text-primary" />
                </div>
                <div className="text-center">
                    <div className="font-medium text-muted-foreground group-hover:text-foreground transition-colors">Add New Provider</div>
                    <div className="text-xs text-muted-foreground/60 mt-1">Configure another news server source</div>
                </div>
            </button>
        </div>
    </div>
  )
}
