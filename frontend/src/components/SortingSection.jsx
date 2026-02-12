import React from 'react'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { FormField, FormItem, FormLabel, FormControl, FormDescription, FormMessage } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { PriorityList, MultiplierSlider } from "@/components/ui/priority-list"
import { useFormContext } from "react-hook-form"

export function SortingSection({ control, watch, fieldPrefix = '' }) {
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
    // Handle both "sorting.field" and just "field" formats
    if (field.startsWith('sorting.')) {
      // If fieldPrefix is already "sorting", return the field as-is
      // react-hook-form needs the full path "sorting.field" to access nested values
      if (fieldPrefix === 'sorting') {
        return field // Return "sorting.field" as-is
      }
      return `${fieldPrefix}.${field}`
    }
    // If fieldPrefix is "sorting" and field doesn't start with "sorting.", prepend it
    if (fieldPrefix === 'sorting') {
      return `sorting.${field}`
    }
    return `${fieldPrefix}.sorting.${field}`
  }
  const resolutionItems = [
    { key: '4k', label: '4K' },
    { key: '1080p', label: '1080p' },
    { key: '720p', label: '720p' },
    { key: 'sd', label: 'SD' },
  ]

  const codecItems = [
    { key: 'HEVC', label: 'HEVC' },
    { key: 'x265', label: 'x265' },
    { key: 'x264', label: 'x264' },
    { key: 'AVC', label: 'AVC' },
  ]

  const audioItems = [
    { key: 'Atmos', label: 'Atmos' },
    { key: 'TrueHD', label: 'TrueHD' },
    { key: 'DTS-HD', label: 'DTS-HD' },
    { key: 'DTS-X', label: 'DTS-X' },
    { key: 'DTS', label: 'DTS' },
    { key: 'DD+', label: 'DD+' },
    { key: 'DD', label: 'DD' },
    { key: 'AC3', label: 'AC3' },
    { key: '5.1', label: '5.1 Channels' },
    { key: '7.1', label: '7.1 Channels' },
  ]

  const qualityItems = [
    { key: 'BluRay', label: 'BluRay' },
    { key: 'Blu-ray', label: 'Blu-ray' },
    { key: 'WEB-DL', label: 'WEB-DL' },
    { key: 'WEBRip', label: 'WEBRip' },
    { key: 'HDTV', label: 'HDTV' },
  ]

  const visualTagItems = [
    { key: 'DV', label: 'DV (Dolby Vision)' },
    { key: 'HDR10+', label: 'HDR10+' },
    { key: 'HDR', label: 'HDR' },
    { key: '3D', label: '3D' },
  ]

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">Sorting Priority</CardTitle>
        <CardDescription>
          Drag items to reorder priority. Items at the top will be prioritized over items at the bottom.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        {/* Resolution Priority */}
        <FormField
          control={actualControl}
          name={getFieldName("sorting.resolution_weights")}
          render={({ field }) => (
            <PriorityList
              items={resolutionItems}
              value={field.value}
              onChange={field.onChange}
              title="Resolution Priority"
              description="Drag to set your preferred resolution order. Higher resolutions typically rank higher."
            />
          )}
        />

        {/* Codec Priority */}
        <FormField
          control={actualControl}
          name={getFieldName("sorting.codec_weights")}
          render={({ field }) => (
            <PriorityList
              items={codecItems}
              value={field.value}
              onChange={field.onChange}
              title="Codec Preference"
              description="Prioritize releases with your preferred video codecs."
            />
          )}
        />

        {/* Audio Priority */}
        <FormField
          control={actualControl}
          name={getFieldName("sorting.audio_weights")}
          render={({ field }) => (
            <PriorityList
              items={audioItems}
              value={field.value}
              onChange={field.onChange}
              title="Audio Format Preference"
              description="Prioritize releases with your preferred audio formats and channel configurations."
            />
          )}
        />

        {/* Quality Priority */}
        <FormField
          control={actualControl}
          name={getFieldName("sorting.quality_weights")}
          render={({ field }) => (
            <PriorityList
              items={qualityItems}
              value={field.value}
              onChange={field.onChange}
              title="Source Quality Preference"
              description="Prioritize releases from your preferred sources."
            />
          )}
        />

        {/* Visual Tag Priority */}
        <FormField
          control={actualControl}
          name={getFieldName("sorting.visual_tag_weights")}
          render={({ field }) => (
            <PriorityList
              items={visualTagItems}
              value={field.value}
              onChange={field.onChange}
              title="Visual Tag Preference"
              description="Prioritize releases with your preferred visual tags (HDR, Dolby Vision, 3D)."
            />
          )}
        />

        {/* Multipliers */}
        <div className="space-y-4 pt-4 border-t">
          <h4 className="font-medium">Preferred Settings</h4>
          <FormField
            control={actualControl}
            name={getFieldName("sorting.preferred_groups")}
            render={({ field }) => (
              <FormItem>
                <FormLabel>Preferred Release Groups</FormLabel>
                <FormControl>
                  <Input
                    placeholder="Enter groups separated by commas (e.g., FLUX, NTb, SWTYBLZ)"
                    value={field.value?.join(', ') || ''}
                    onChange={(e) => field.onChange(e.target.value.split(',').map(s => s.trim()).filter(Boolean))}
                  />
                </FormControl>
                <FormDescription>Boost score for releases from these groups (+1000 points)</FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={actualControl}
            name={getFieldName("sorting.preferred_languages")}
            render={({ field }) => (
              <FormItem>
                <FormLabel>Preferred Languages</FormLabel>
                <FormControl>
                  <Input
                    placeholder="Enter language codes separated by commas (e.g., en, multi)"
                    value={field.value?.join(', ') || ''}
                    onChange={(e) => field.onChange(e.target.value.split(',').map(s => s.trim()).filter(Boolean))}
                  />
                </FormControl>
                <FormDescription>Boost score for releases in these languages (future feature)</FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </div>

        {/* Multipliers */}
        <div className="space-y-4 pt-4 border-t">
          <h4 className="font-medium">Score Multipliers</h4>
          <FormField
            control={actualControl}
            name={getFieldName("sorting.grab_weight")}
            render={({ field }) => (
              <MultiplierSlider
                label="Grab Weight"
                value={field.value || 0.5}
                onChange={field.onChange}
                min={0}
                max={2}
                step={0.1}
                description="How much to prioritize popular releases (higher grabs = more popular)"
              />
            )}
          />
          <FormField
            control={actualControl}
            name={getFieldName("sorting.age_weight")}
            render={({ field }) => (
              <MultiplierSlider
                label="Age Weight"
                value={field.value || 1.0}
                onChange={field.onChange}
                min={0}
                max={2}
                step={0.1}
                description="How much to prioritize newer releases (higher = prefer newer)"
              />
            )}
          />
        </div>
      </CardContent>
    </Card>
  )
}
