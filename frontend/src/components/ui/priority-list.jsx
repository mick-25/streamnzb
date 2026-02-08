import React, { useState, useEffect } from 'react'
import { DraggableList } from '@/components/ui/draggable-list'
import { Slider } from '@/components/ui/slider'

export function PriorityList({ items, value = {}, onChange, title, description }) {
  // Convert weight map to ordered array
  const [orderedItems, setOrderedItems] = useState(() => {
    // Sort items by their weight (descending)
    return [...items].sort((a, b) => (value[a.key] || 0) - (value[b.key] || 0)).reverse()
  })

  // Update ordered items when value changes externally
  useEffect(() => {
    const newOrder = [...items].sort((a, b) => (value[a.key] || 0) - (value[b.key] || 0)).reverse()
    setOrderedItems(newOrder)
  }, [value, items])

  const handleReorder = (newOrder) => {
    setOrderedItems(newOrder)
    
    // Convert back to weight map
    const weights = {}
    const baseWeight = 1000000
    newOrder.forEach((item, index) => {
      // Reverse index so first item gets highest weight
      const position = newOrder.length - index
      weights[item.key] = baseWeight * position
    })
    
    onChange(weights)
  }

  // Convert items to format expected by DraggableList
  const draggableItems = orderedItems.map(item => ({
    id: item.key,
    label: item.label,
  }))

  return (
    <div className="space-y-3">
      <div>
        <h4 className="font-medium">{title}</h4>
        {description && <p className="text-sm text-muted-foreground mt-1">{description}</p>}
      </div>
      <DraggableList items={draggableItems} onReorder={(newItems) => {
        // Convert back to our item format
        const newOrder = newItems.map(dItem => orderedItems.find(item => item.key === dItem.id))
        handleReorder(newOrder)
      }} />
    </div>
  )
}

export function MultiplierSlider({ label, value = 1.0, onChange, min = 0, max = 2, step = 0.1, description }) {
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div>
          <h4 className="font-medium">{label}</h4>
          {description && <p className="text-sm text-muted-foreground">{description}</p>}
        </div>
        <span className="text-sm font-mono bg-muted px-2 py-1 rounded">{value.toFixed(1)}</span>
      </div>
      <Slider
        value={[value]}
        onValueChange={(values) => onChange(values[0])}
        min={min}
        max={max}
        step={step}
        className="w-full"
      />
    </div>
  )
}
