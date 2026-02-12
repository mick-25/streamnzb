import React from 'react'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { FormField, FormItem, FormLabel, FormControl, FormMessage, FormDescription } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import { Badge } from "@/components/ui/badge"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { X, Info } from "lucide-react"
import { useFormContext } from "react-hook-form"

const QUALITY_OPTIONS = ['BluRay', 'BluRay REMUX', 'WEB-DL', 'WEBRip', 'HDTV', 'DVDRip', 'BRRip']
const BLOCKED_QUALITY_OPTIONS = ['CAM', 'TeleSync', 'TeleCine', 'SCR']
const RESOLUTION_OPTIONS = ['2160p', '1080p', '720p', '480p']
const CODEC_OPTIONS = ['HEVC', 'AVC', 'MPEG-2']
const AUDIO_OPTIONS = ['Atmos', 'TrueHD', 'DTS Lossless', 'DTS Lossy', 'DDP', 'DD', 'AAC']
const VISUAL_TAG_OPTIONS = ['DV', 'HDR10+', 'HDR', '3D']
const LANGUAGE_OPTIONS = ['en', 'multi', 'dual audio']

// PTT Reference values for tooltips
const PTT_VALUES = {
  quality: {
    cam: ['CAM', 'TeleSync', 'TeleCine', 'SCR'],
    web: ['WEB', 'WEB-DL', 'WEBRip', 'WEB-DLRip'],
    broadcast: ['HDTV', 'HDTVRip', 'PDTV', 'TVRip', 'SATRip'],
    physical: ['BluRay', 'BluRay REMUX', 'REMUX', 'BRRip', 'BDRip', 'UHDRip', 'HDRip', 'DVD', 'DVDRip', 'PPVRip', 'R5'],
    other: ['XviD', 'DivX']
  },
  resolution: ['4k (2160p)', '2k (1440p)', '1080p', '720p', '576p', '480p', '360p', '240p'],
  codec: {
    AVC: ['avc', 'h264', 'x264'],
    HEVC: ['hevc', 'h265', 'x265'],
    'MPEG-2': ['mpeg2'],
    DivX: ['divx', 'dvix'],
    Xvid: ['xvid']
  },
  audio: ['DTS Lossless', 'DTS Lossy', 'Atmos', 'TrueHD', 'FLAC', 'DDP', 'EAC3', 'DD', 'AC3', 'AAC', 'PCM', 'OPUS', 'HQ', 'MP3'],
  channels: ['2.0', '5.1', '7.1', 'stereo', 'mono'],
  visualTags: {
    hdr: ['DV', 'HDR10+', 'HDR', 'SDR'],
    threeD: ['3D', '3D HSBS', '3D SBS', '3D HOU', '3D OU']
  },
  languages: {
    special: ['multi subs', 'multi audio', 'dual audio'],
    iso6391: ['en', 'ja', 'ko', 'zh', 'zh-tw', 'fr', 'es', 'es-419', 'pt', 'it', 'de', 'ru', 'uk', 'nl', 'da', 'fi', 'sv', 'no', 'el', 'lt', 'lv', 'et', 'pl', 'cs', 'sk', 'hu', 'ro', 'bg', 'sr', 'hr', 'sl', 'hi', 'te', 'ta', 'ml', 'kn', 'mr', 'gu', 'pa', 'bn', 'vi', 'id', 'th', 'ms', 'ar', 'tr', 'he', 'fa']
  },
  bitDepth: ['8bit', '10bit', '12bit']
}

// Helper component for labels with tooltips
function LabelWithTooltip({ label, tooltipContent, className }) {
  return (
    <div className="flex items-center gap-2">
      <FormLabel className={className}>{label}</FormLabel>
      <Tooltip>
        <TooltipTrigger asChild>
          <Info className="h-4 w-4 text-muted-foreground cursor-help hover:text-foreground" />
        </TooltipTrigger>
        <TooltipContent 
          className="max-w-md p-3 z-[100]"
          side="bottom"
          sideOffset={5}
          avoidCollisions={true}
          collisionPadding={8}
        >
          <div className="text-sm space-y-1">
            {tooltipContent}
          </div>
        </TooltipContent>
      </Tooltip>
    </div>
  )
}

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

export function FiltersSection({ control, watch, fieldPrefix = '' }) {
  // Use form context if control/watch not provided (for nested forms)
  let formContext = null
  try {
    formContext = useFormContext()
  } catch (e) {
    // Not in form context, use provided props
  }
  
  const actualControl = control || formContext?.control
  const actualWatch = watch || formContext?.watch
  
  const getFieldName = (field) => {
    if (!fieldPrefix) return field
    // Handle both "filters.field" and just "field" formats
    if (field.startsWith('filters.')) {
      // If fieldPrefix is already "filters", return the field as-is
      // react-hook-form needs the full path "filters.max_resolution" to access nested values
      if (fieldPrefix === 'filters') {
        return field // Return "filters.max_resolution" as-is
      }
      return `${fieldPrefix}.${field}`
    }
    // If fieldPrefix is "filters" and field doesn't start with "filters.", prepend it
    if (fieldPrefix === 'filters') {
      return `filters.${field}`
    }
    return `${fieldPrefix}.filters.${field}`
  }
  return (
    <TooltipProvider>
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
          control={actualControl}
          name={getFieldName("filters.allowed_qualities")}
                render={({ field }) => (
                  <FormItem>
                    <LabelWithTooltip 
                      label="Allowed Qualities"
                      tooltipContent={
                        <div>
                          <div className="font-semibold mb-1">Possible values:</div>
                          <div><strong>Cam:</strong> {PTT_VALUES.quality.cam.join(', ')}</div>
                          <div><strong>Web:</strong> {PTT_VALUES.quality.web.join(', ')}</div>
                          <div><strong>Broadcast:</strong> {PTT_VALUES.quality.broadcast.join(', ')}</div>
                          <div><strong>Physical:</strong> {PTT_VALUES.quality.physical.join(', ')}</div>
                          <div><strong>Other:</strong> {PTT_VALUES.quality.other.join(', ')}</div>
                        </div>
                      }
                    />
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
                control={actualControl}
                name={getFieldName("filters.blocked_qualities")}
                render={({ field }) => (
                  <FormItem>
                    <LabelWithTooltip 
                      label="Blocked Qualities"
                      tooltipContent={
                        <div>
                          <div className="font-semibold mb-1">Possible values:</div>
                          <div><strong>Cam:</strong> {PTT_VALUES.quality.cam.join(', ')}</div>
                          <div><strong>Web:</strong> {PTT_VALUES.quality.web.join(', ')}</div>
                          <div><strong>Broadcast:</strong> {PTT_VALUES.quality.broadcast.join(', ')}</div>
                          <div><strong>Physical:</strong> {PTT_VALUES.quality.physical.join(', ')}</div>
                          <div><strong>Other:</strong> {PTT_VALUES.quality.other.join(', ')}</div>
                        </div>
                      }
                    />
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
                control={actualControl}
                name={getFieldName("filters.block_cam")}
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
                  control={actualControl}
                  name={getFieldName("filters.min_resolution")}
                  render={({ field }) => (
                    <FormItem>
                      <LabelWithTooltip 
                        label="Minimum Resolution"
                        tooltipContent={
                          <div>
                            <div className="font-semibold mb-1">Possible values:</div>
                            <div>{PTT_VALUES.resolution.join(', ')}</div>
                          </div>
                        }
                      />
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
                  control={actualControl}
                  name={getFieldName("filters.max_resolution")}
                  render={({ field }) => (
                    <FormItem>
                      <LabelWithTooltip 
                        label="Maximum Resolution"
                        tooltipContent={
                          <div>
                            <div className="font-semibold mb-1">Possible values:</div>
                            <div>{PTT_VALUES.resolution.join(', ')}</div>
                          </div>
                        }
                      />
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
                control={actualControl}
                name={getFieldName("filters.allowed_codecs")}
                render={({ field }) => (
                  <FormItem>
                    <LabelWithTooltip 
                      label="Allowed Codecs"
                      tooltipContent={
                        <div>
                          <div className="font-semibold mb-1">Possible values:</div>
                          <div><strong>AVC:</strong> {PTT_VALUES.codec.AVC.join(', ')}</div>
                          <div><strong>HEVC:</strong> {PTT_VALUES.codec.HEVC.join(', ')}</div>
                          <div><strong>MPEG-2:</strong> {PTT_VALUES.codec['MPEG-2'].join(', ')}</div>
                          <div><strong>DivX:</strong> {PTT_VALUES.codec.DivX.join(', ')}</div>
                          <div><strong>Xvid:</strong> {PTT_VALUES.codec.Xvid.join(', ')}</div>
                        </div>
                      }
                    />
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
              <FormField
                control={actualControl}
                name={getFieldName("filters.blocked_codecs")}
                render={({ field }) => (
                  <FormItem>
                    <LabelWithTooltip 
                      label="Blocked Codecs"
                      tooltipContent={
                        <div>
                          <div className="font-semibold mb-1">Possible values:</div>
                          <div><strong>AVC:</strong> {PTT_VALUES.codec.AVC.join(', ')}</div>
                          <div><strong>HEVC:</strong> {PTT_VALUES.codec.HEVC.join(', ')}</div>
                          <div><strong>MPEG-2:</strong> {PTT_VALUES.codec['MPEG-2'].join(', ')}</div>
                          <div><strong>DivX:</strong> {PTT_VALUES.codec.DivX.join(', ')}</div>
                          <div><strong>Xvid:</strong> {PTT_VALUES.codec.Xvid.join(', ')}</div>
                        </div>
                      }
                    />
                    <FormControl>
                      <MultiSelectBadges
                        value={field.value || []}
                        onChange={field.onChange}
                        options={CODEC_OPTIONS}
                        placeholder="Add blocked codec..."
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            {/* Audio Filters */}
            <div className="space-y-4">
              <h4 className="font-medium">Audio</h4>
              <FormField
                control={actualControl}
                name={getFieldName("filters.allowed_audio")}
                render={({ field }) => (
                  <FormItem>
                    <LabelWithTooltip 
                      label="Allowed Audio Formats"
                      tooltipContent={
                        <div>
                          <div className="font-semibold mb-1">Possible values:</div>
                          <div>{PTT_VALUES.audio.join(', ')}</div>
                        </div>
                      }
                    />
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
                control={actualControl}
                name={getFieldName("filters.min_channels")}
                render={({ field }) => (
                  <FormItem>
                    <LabelWithTooltip 
                      label="Minimum Channels"
                      tooltipContent={
                        <div>
                          <div className="font-semibold mb-1">Possible values:</div>
                          <div>{PTT_VALUES.channels.join(', ')}</div>
                        </div>
                      }
                    />
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

            {/* Visual Tags Filters */}
            <div className="space-y-4">
              <h4 className="font-medium">Visual Tags</h4>
              <FormField
                control={actualControl}
                name={getFieldName("filters.require_hdr")}
                render={({ field }) => (
                  <FormItem className="flex flex-row items-center space-x-3 space-y-0">
                    <FormControl>
                      <Checkbox
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                    <FormLabel className="font-normal">
                      Require Visual Tag
                    </FormLabel>
                  </FormItem>
                )}
              />
              <FormField
                control={actualControl}
                name={getFieldName("filters.allowed_hdr")}
                render={({ field }) => (
                  <FormItem>
                    <LabelWithTooltip 
                      label="Allowed Visual Tags"
                      tooltipContent={
                        <div>
                          <div className="font-semibold mb-1">Possible values:</div>
                          <div><strong>HDR:</strong> {PTT_VALUES.visualTags.hdr.join(', ')}</div>
                          <div><strong>3D:</strong> {PTT_VALUES.visualTags.threeD.join(', ')}</div>
                        </div>
                      }
                    />
                    <FormControl>
                      <MultiSelectBadges
                        value={field.value || []}
                        onChange={field.onChange}
                        options={VISUAL_TAG_OPTIONS}
                        placeholder="Add visual tag..."
                      />
                    </FormControl>
                    <FormDescription>Leave empty to allow all visual tags</FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={actualControl}
                name={getFieldName("filters.blocked_hdr")}
                render={({ field }) => (
                  <FormItem>
                    <LabelWithTooltip 
                      label="Blocked Visual Tags"
                      tooltipContent={
                        <div>
                          <div className="font-semibold mb-1">Possible values:</div>
                          <div><strong>HDR:</strong> {PTT_VALUES.visualTags.hdr.join(', ')}</div>
                          <div><strong>3D:</strong> {PTT_VALUES.visualTags.threeD.join(', ')}</div>
                        </div>
                      }
                    />
                    <FormControl>
                      <MultiSelectBadges
                        value={field.value || []}
                        onChange={field.onChange}
                        options={VISUAL_TAG_OPTIONS}
                        placeholder="Add blocked visual tag (e.g., DV)..."
                      />
                    </FormControl>
                    <FormDescription>Block specific visual tags like Dolby Vision (DV) or 3D</FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={actualControl}
                name={getFieldName("filters.block_sdr")}
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
                control={actualControl}
                name={getFieldName("filters.allowed_languages")}
                render={({ field }) => (
                  <FormItem>
                    <LabelWithTooltip 
                      label="Allowed Languages"
                      tooltipContent={
                        <div>
                          <div className="font-semibold mb-1">Possible values:</div>
                          <div><strong>Special:</strong> {PTT_VALUES.languages.special.join(', ')}</div>
                          <div><strong>ISO 639-1:</strong> {PTT_VALUES.languages.iso6391.slice(0, 20).join(', ')}...</div>
                          <div className="text-xs mt-1">(and more: {PTT_VALUES.languages.iso6391.slice(20).join(', ')})</div>
                        </div>
                      }
                    />
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
                  control={actualControl}
                  name={getFieldName("filters.min_size_gb")}
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
                  control={actualControl}
                  name={getFieldName("filters.max_size_gb")}
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
                control={actualControl}
                name={getFieldName("filters.blocked_groups")}
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
    </TooltipProvider>
  )
}
