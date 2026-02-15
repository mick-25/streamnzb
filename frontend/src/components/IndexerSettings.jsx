import React from 'react'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { FormField, FormItem, FormLabel, FormControl, FormMessage, FormDescription } from "@/components/ui/form"
import { PasswordInput } from "@/components/ui/password-input"
import { Trash2, Plus } from "lucide-react"

const INDEXER_PRESETS = [
    { name: 'abNZB', url: 'https://abnzb.com', api_path: '/api', type: 'newznab' },
    { name: 'altHUB', url: 'https://api.althub.co.za', api_path: '/api', type: 'newznab' },
    { name: 'AnimeTosho (Usenet)', url: 'https://feed.animetosho.org', api_path: '/api', type: 'newznab' },
    { name: 'DOGnzb', url: 'https://api.dognzb.cr', api_path: '/api', type: 'newznab' },
    { name: 'DrunkenSlug', url: 'https://drunkenslug.com', api_path: '/api', type: 'newznab' },
    { name: 'GingaDADDY', url: 'https://www.gingadaddy.com', api_path: '/api', type: 'newznab' },
    { name: 'Miatrix', url: 'https://www.miatrix.com', api_path: '/api', type: 'newznab' },
    { name: 'Newz69', url: 'https://newz69.keagaming.com', api_path: '/api', type: 'newznab' },
    { name: 'NinjaCentral', url: 'https://ninjacentral.co.za', api_path: '/api', type: 'newznab' },
    { name: 'Nzb.life', url: 'https://api.nzb.life', api_path: '/api', type: 'newznab' },
    { name: 'NZBCat', url: 'https://nzb.cat', api_path: '/api', type: 'newznab' },
    { name: 'NZBFinder', url: 'https://nzbfinder.ws', api_path: '/api', type: 'newznab' },
    { name: 'NZBgeek', url: 'https://api.nzbgeek.info', api_path: '/api', type: 'newznab' },
    { name: 'NzbNoob', url: 'https://www.nzbnoob.com', api_path: '/api', type: 'newznab' },
    { name: 'NZBNDX', url: 'https://www.nzbndx.com', api_path: '/api', type: 'newznab' },
    { name: 'NzbPlanet', url: 'https://api.nzbplanet.net', api_path: '/api', type: 'newznab' },
    { name: 'NZBStars', url: 'https://nzbstars.com', api_path: '/api', type: 'newznab' },
    { name: 'SceneNZBs', url: 'https://scenenzbs.com', api_path: '/api', type: 'newznab' },
    { name: 'Tabula Rasa', url: 'https://www.tabula-rasa.pw', api_path: '/api/v1', type: 'newznab' },
    { name: 'Usenet Crawler', url: 'https://www.usenet-crawler.com', api_path: '/api', type: 'newznab' },
    { name: 'Prowlarr', url: '', api_path: '/api', type: 'prowlarr' },
    { name: 'NZBHydra2', url: '', api_path: '/api', type: 'nzbhydra' },
    //{ name: 'Easynews (Experimental)', url: '', api_path: '/api', type: 'easynews' },
    { name: 'Custom Newznab', url: '', api_path: '/api', type: 'newznab' }
]

export function IndexerSettings({ control, indexerFields, appendIndexer, removeIndexer, watch, setValue }) {
  return (
    <div className="space-y-6">
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            {indexerFields.map((field, index) => {
                const currentType = watch(`indexers.${index}.type`) || 'newznab';
                const isMeta = currentType === 'prowlarr' || currentType === 'nzbhydra';
                const isEasynews = currentType === 'easynews';

                return (
                    <Card key={field.id} className="relative flex flex-col h-full">
                        <Button
                            type="button"
                            variant="ghost"
                            size="icon"
                            className="absolute right-1 top-1 h-8 w-8 text-destructive hover:text-destructive/90 z-10"
                            onClick={() => removeIndexer(index)}
                        >
                            <Trash2 className="h-4 w-4" />
                        </Button>
                        <CardHeader className="pb-3">
                            <CardTitle className="text-base truncate pr-8">
                                {watch(`indexers.${index}.name`) || `Indexer ${index + 1}`}
                            </CardTitle>
                        </CardHeader>
                        <CardContent className="space-y-4 flex-grow">
                            <div className="space-y-2">
                                <Label>Preset / Type</Label>
                                <select
                                    className="flex h-9 w-full items-center justify-between rounded-md border border-input bg-background px-3 py-1 text-sm ring-offset-background placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
                                    onChange={(e) => {
                                        const preset = INDEXER_PRESETS.find(p => p.name === e.target.value);
                                        if (preset) {
                                            setValue(`indexers.${index}.name`, preset.name);
                                            setValue(`indexers.${index}.url`, preset.url);
                                            setValue(`indexers.${index}.api_path`, preset.api_path || '/api');
                                            setValue(`indexers.${index}.type`, preset.type);
                                        }
                                    }}
                                    value={INDEXER_PRESETS.find(p => p.name === watch(`indexers.${index}.name`))?.name || (watch(`indexers.${index}.type`) === 'prowlarr' ? 'Prowlarr' : watch(`indexers.${index}.type`) === 'nzbhydra' ? 'NZBHydra2' : watch(`indexers.${index}.type`) === 'easynews' ? 'Easynews (Experimental)' : 'Custom Newznab')}
                                >
                                    {INDEXER_PRESETS.map(preset => (
                                        <option key={preset.name} value={preset.name}>{preset.name}</option>
                                    ))}
                                </select>
                            </div>
                            
                            {!isEasynews && (
                                <FormField
                                    control={control}
                                    name={`indexers.${index}.url`}
                                    render={({ field }) => (
                                        <FormItem>
                                            <FormLabel>URL</FormLabel>
                                            <FormControl>
                                                <Input placeholder="https://api.nzbgeek.info" className="h-8 text-xs" {...field} />
                                            </FormControl>
                                            <FormMessage />
                                        </FormItem>
                                    )}
                                />
                            )}
                            
                            {!isMeta && !isEasynews && (
                                <FormField
                                    control={control}
                                    name={`indexers.${index}.api_path`}
                                    render={({ field }) => (
                                        <FormItem>
                                            <FormLabel>API Path</FormLabel>
                                            <FormControl>
                                                <Input placeholder="/api" className="h-8 text-xs" {...field} />
                                            </FormControl>
                                            <FormDescription className="text-[10px]">API endpoint path (default: /api, Tabula Rasa: /api/v1)</FormDescription>
                                            <FormMessage />
                                        </FormItem>
                                    )}
                                />
                            )}
                            
                            {!isEasynews ? (
                                <FormField
                                    control={control}
                                    name={`indexers.${index}.api_key`}
                                    render={({ field }) => (
                                        <FormItem>
                                            <FormLabel>API Key</FormLabel>
                                            <FormControl>
                                                <PasswordInput className="h-8 text-xs" {...field} />
                                            </FormControl>
                                            <FormMessage />
                                        </FormItem>
                                    )}
                                />
                            ) : (
                                <>
                                    <FormField
                                        control={control}
                                        name={`indexers.${index}.username`}
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>Username</FormLabel>
                                                <FormControl>
                                                    <Input className="h-8 text-xs" {...field} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                    <FormField
                                        control={control}
                                        name={`indexers.${index}.password`}
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>Password</FormLabel>
                                                <FormControl>
                                                    <PasswordInput className="h-8 text-xs" {...field} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                </>
                            )}

                            {!isMeta && !isEasynews && (
                                <div className="grid grid-cols-2 gap-2 mt-2">
                                    <FormField
                                        control={control}
                                        name={`indexers.${index}.api_hits_day`}
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel className="text-[10px]">Hits/Day</FormLabel>
                                                <FormControl>
                                                    <Input type="number" placeholder="100" className="h-8 text-xs" {...field} onChange={e => field.onChange(Number(e.target.value))} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                    <FormField
                                        control={control}
                                        name={`indexers.${index}.downloads_day`}
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel className="text-[10px]">DLs/Day</FormLabel>
                                                <FormControl>
                                                    <Input type="number" placeholder="50" className="h-8 text-xs" {...field} onChange={e => field.onChange(Number(e.target.value))} />
                                                </FormControl>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                </div>
                            )}
                        </CardContent>
                    </Card>
                )
            })}

            {/* Skeleton Add Card */}
            <button
                type="button"
                onClick={() => appendIndexer({ name: '', url: '', api_path: '/api', api_key: '', type: 'newznab', api_hits_day: 0, downloads_day: 0, username: '', password: '' })}
                className="flex flex-col items-center justify-center p-6 border-2 border-dashed rounded-lg border-muted-foreground/25 hover:border-muted-foreground/50 hover:bg-accent/50 transition-all min-h-[250px] group"
            >
                <div className="flex items-center justify-center w-12 h-12 rounded-full bg-primary/10 group-hover:bg-primary/20 transition-colors mb-4">
                    <Plus className="w-6 h-6 text-primary" />
                </div>
                <div className="text-center">
                    <div className="font-medium text-muted-foreground group-hover:text-foreground transition-colors">Add New Indexer</div>
                    <div className="text-xs text-muted-foreground/60 mt-1">Configure another search source</div>
                </div>
            </button>
        </div>

        {indexerFields.length === 0 && (
            <div className="hidden">
                {/* This is handled by the skeleton card now */}
            </div>
        )}
    </div>
  )
}
