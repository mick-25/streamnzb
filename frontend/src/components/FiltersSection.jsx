import React from 'react'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { FormField, FormItem, FormLabel, FormControl, FormMessage, FormDescription } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import { Badge } from "@/components/ui/badge"
import { X } from "lucide-react"

const QUALITY_OPTIONS = ['BluRay', 'BluRay REMUX', 'WEB-DL', 'WEBRip', 'HDTV', 'DVDRip', 'BRRip']
const BLOCKED_QUALITY_OPTIONS = ['CAM', 'TeleSync', 'TeleCine', 'SCR']
const RESOLUTION_OPTIONS = ['2160p', '1080p', '720p', '480p']
const CODEC_OPTIONS = ['HEVC', 'AVC', 'MPEG-2']
const AUDIO_OPTIONS = ['Atmos', 'TrueHD', 'DTS Lossless', 'DTS Lossy', 'DDP', 'DD', 'AAC']
const HDR_OPTIONS = ['DV', 'HDR10+', 'HDR']
const LANGUAGE_OPTIONS = ['en', 'multi', 'dual audio']

function MultiSelectBadges({ value = [], onChange, options, placeholder }) {
  const [inputValue, setInputValue] = React.useState('')
  
  const addItem = (item) => {
    if (item && !value.includes(item)) {
      onChange([...value, item])
    }
  }
  
  const removeItem = (item) => {
    onChange(value.filter(v => v !== item))
  }
  
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-2 min-h-[2.5rem] p-2 border rounded-md">
        {value.map(item => (
          <Badge key={item} variant="secondary" className="gap-1">
            {item}
            <X className="h-3 w-3 cursor-pointer" onClick={() => removeItem(item)} />
          </Badge>
        ))}
      </div>
      <div className="flex gap-2">
        <Input
          value={inputValue}
          onChange={(e) => setInputValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              addItem(inputValue)
              setInputValue('')
            }
          }}
          placeholder={placeholder || "Type and press Enter"}
          className="flex-1"
        />
        <div className="flex flex-wrap gap-1">
          {options.filter(opt => !value.includes(opt)).slice(0, 5).map(opt => (
            <Badge
              key={opt}
              variant="outline"
              className="cursor-pointer hover:bg-accent"
              onClick={() => {
                addItem(opt)
                setInputValue('')
              }}
            >
              {opt}
            </Badge>
          ))}
        </div>
      </div>
    </div>
  )
}

export function FiltersSection({ control, watch }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">Release Filters</CardTitle>
        <CardDescription>
          Filter search results based on quality, codec, audio, and other release attributes. Leave fields empty to allow all.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
            {/* Quality Filters */}
            <div className="space-y-4">
              <h4 className="font-medium">Quality</h4>
              <FormField
                control={control}
                name="filters.allowed_qualities"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Allowed Qualities</FormLabel>
                    <FormControl>
                      <MultiSelectBadges
                        value={field.value || []}
                        onChange={field.onChange}
                        options={QUALITY_OPTIONS}
                        placeholder="Add allowed quality..."
                      />
                    </FormControl>
                    <FormDescription>Leave empty to allow all</FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={control}
                name="filters.blocked_qualities"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Blocked Qualities</FormLabel>
                    <FormControl>
                      <MultiSelectBadges
                        value={field.value || []}
                        onChange={field.onChange}
                        options={BLOCKED_QUALITY_OPTIONS}
                        placeholder="Add blocked quality..."
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={control}
                name="filters.block_cam"
                render={({ field }) => (
                  <FormItem className="flex flex-row items-center space-x-3 space-y-0">
                    <FormControl>
                      <Checkbox
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                    <FormLabel className="font-normal">
                      Block CAM/TS/TC releases
                    </FormLabel>
                  </FormItem>
                )}
              />
            </div>

            {/* Resolution Filters */}
            <div className="space-y-4">
              <h4 className="font-medium">Resolution</h4>
              <div className="grid grid-cols-2 gap-4">
                <FormField
                  control={control}
                  name="filters.min_resolution"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Minimum Resolution</FormLabel>
                      <FormControl>
                        <select
                          className="flex h-10 w-full items-center justify-between rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring"
                          {...field}
                        >
                          <option value="">Any</option>
                          {RESOLUTION_OPTIONS.map(res => (
                            <option key={res} value={res}>{res}</option>
                          ))}
                        </select>
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={control}
                  name="filters.max_resolution"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Maximum Resolution</FormLabel>
                      <FormControl>
                        <select
                          className="flex h-10 w-full items-center justify-between rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring"
                          {...field}
                        >
                          <option value="">Any</option>
                          {RESOLUTION_OPTIONS.map(res => (
                            <option key={res} value={res}>{res}</option>
                          ))}
                        </select>
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>
            </div>

            {/* Codec Filters */}
            <div className="space-y-4">
              <h4 className="font-medium">Codec</h4>
              <FormField
                control={control}
                name="filters.allowed_codecs"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Allowed Codecs</FormLabel>
                    <FormControl>
                      <MultiSelectBadges
                        value={field.value || []}
                        onChange={field.onChange}
                        options={CODEC_OPTIONS}
                        placeholder="Add allowed codec..."
                      />
                    </FormControl>
                    <FormDescription>Leave empty to allow all</FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            {/* Audio Filters */}
            <div className="space-y-4">
              <h4 className="font-medium">Audio</h4>
              <FormField
                control={control}
                name="filters.allowed_audio"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Allowed Audio Formats</FormLabel>
                    <FormControl>
                      <MultiSelectBadges
                        value={field.value || []}
                        onChange={field.onChange}
                        options={AUDIO_OPTIONS}
                        placeholder="Add allowed audio..."
                      />
                    </FormControl>
                    <FormDescription>Leave empty to allow all</FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={control}
                name="filters.min_channels"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Minimum Channels</FormLabel>
                    <FormControl>
                      <select
                        className="flex h-10 w-full items-center justify-between rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring"
                        {...field}
                      >
                        <option value="">Any</option>
                        <option value="2.0">2.0 (Stereo)</option>
                        <option value="5.1">5.1</option>
                        <option value="7.1">7.1</option>
                      </select>
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            {/* HDR Filters */}
            <div className="space-y-4">
              <h4 className="font-medium">HDR</h4>
              <FormField
                control={control}
                name="filters.require_hdr"
                render={({ field }) => (
                  <FormItem className="flex flex-row items-center space-x-3 space-y-0">
                    <FormControl>
                      <Checkbox
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                    <FormLabel className="font-normal">
                      Require HDR
                    </FormLabel>
                  </FormItem>
                )}
              />
              <FormField
                control={control}
                name="filters.allowed_hdr"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Allowed HDR Types</FormLabel>
                    <FormControl>
                      <MultiSelectBadges
                        value={field.value || []}
                        onChange={field.onChange}
                        options={HDR_OPTIONS}
                        placeholder="Add HDR type..."
                      />
                    </FormControl>
                    <FormDescription>Leave empty to allow all HDR types</FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={control}
                name="filters.blocked_hdr"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Blocked HDR Types</FormLabel>
                    <FormControl>
                      <MultiSelectBadges
                        value={field.value || []}
                        onChange={field.onChange}
                        options={HDR_OPTIONS}
                        placeholder="Add blocked HDR type (e.g., DV)..."
                      />
                    </FormControl>
                    <FormDescription>Block specific HDR types like Dolby Vision (DV)</FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={control}
                name="filters.block_sdr"
                render={({ field }) => (
                  <FormItem className="flex flex-row items-center space-x-3 space-y-0">
                    <FormControl>
                      <Checkbox
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                    <FormLabel className="font-normal">
                      Block SDR releases
                    </FormLabel>
                  </FormItem>
                )}
              />
            </div>

            {/* Language Filters */}
            <div className="space-y-4">
              <h4 className="font-medium">Languages</h4>
              <FormField
                control={control}
                name="filters.allowed_languages"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Allowed Languages</FormLabel>
                    <FormControl>
                      <MultiSelectBadges
                        value={field.value || []}
                        onChange={field.onChange}
                        options={LANGUAGE_OPTIONS}
                        placeholder="Add language code..."
                      />
                    </FormControl>
                    <FormDescription>Leave empty to allow all</FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            {/* Size Filters */}
            <div className="space-y-4">
              <h4 className="font-medium">File Size</h4>
              <div className="grid grid-cols-2 gap-4">
                <FormField
                  control={control}
                  name="filters.min_size_gb"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Minimum Size (GB)</FormLabel>
                      <FormControl>
                        <Input
                          type="number"
                          step="0.1"
                          placeholder="0"
                          {...field}
                          onChange={e => field.onChange(e.target.valueAsNumber || 0)}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={control}
                  name="filters.max_size_gb"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Maximum Size (GB)</FormLabel>
                      <FormControl>
                        <Input
                          type="number"
                          step="0.1"
                          placeholder="0"
                          {...field}
                          onChange={e => field.onChange(e.target.valueAsNumber || 0)}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>
            </div>

            {/* Group Filters */}
            <div className="space-y-4">
              <h4 className="font-medium">Release Groups</h4>
              <FormField
                control={control}
                name="filters.preferred_groups"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Preferred Groups</FormLabel>
                    <FormControl>
                      <MultiSelectBadges
                        value={field.value || []}
                        onChange={field.onChange}
                        options={[]}
                        placeholder="Add group name..."
                      />
                    </FormControl>
                    <FormDescription>Boost score for these groups</FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={control}
                name="filters.blocked_groups"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>Blocked Groups</FormLabel>
                    <FormControl>
                      <MultiSelectBadges
                        value={field.value || []}
                        onChange={field.onChange}
                        options={[]}
                        placeholder="Add group name..."
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
      </CardContent>
    </Card>
  )
}
